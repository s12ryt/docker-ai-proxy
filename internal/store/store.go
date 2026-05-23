package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store wraps a SQL database used for request logs and aggregated metrics. It
// supports SQLite (default, file-based, pure-go via modernc.org/sqlite), MySQL
// (github.com/go-sql-driver/mysql) and PostgreSQL (jackc/pgx in database/sql
// mode). Schema and placeholder differences are encapsulated in the dialect.
//
// Concurrency: database/sql already arbitrates connection use; for SQLite we
// additionally pin MaxOpenConns=1 (see applyPool) so the file is only written
// by one goroutine at a time. No application-level mutex is needed — the old
// global sync.Mutex serialised every cloud-DB write and was the chief
// throughput ceiling under load.
type Store struct {
	db      *sql.DB
	dialect dialect
}

// Config controls how the underlying database connection is opened.
//
// Driver selects the SQL flavour. Values: "sqlite" (default, empty also
// accepted), "mysql", "postgres". DSN is the driver-specific connection
// string. For SQLite, DSN is the file path (Path is accepted as a legacy
// alias). For MySQL the DSN follows go-sql-driver/mysql's format, e.g.
//
//	user:pass@tcp(host:3306)/dbname?parseTime=true&charset=utf8mb4&loc=UTC
//
// For PostgreSQL it follows pgx's URL form, e.g.
//
//	postgres://user:pass@host:5432/dbname?sslmode=require
//
// MaxOpenConns / MaxIdleConns / ConnMaxLifetime tune the connection pool. The
// defaults are conservative and safe for embedded SQLite as well as cloud
// MySQL/Postgres (which typically expose ~100 connections to the database
// user). Set MaxOpenConns to a positive number to override.
type Config struct {
	Driver          string
	DSN             string
	Path            string // legacy alias for DSN when Driver == "sqlite"
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// CallRecord describes a single upstream call.
type CallRecord struct {
	ID         int64
	Timestamp  time.Time
	Provider   string
	Model      string
	Path       string
	Status     int
	LatencyMS  int64
	BytesIn    int64
	BytesOut   int64
	TokensIn   int64
	TokensOut  int64
	ClientIP   string
	ErrMessage string
}

// Open initialises the store and applies the schema. For backwards
// compatibility a bare file path is still accepted via OpenSQLite.
func Open(cfg Config) (*Store, error) {
	d, err := resolveDialect(cfg.Driver)
	if err != nil {
		return nil, err
	}

	dsn, err := resolveDSN(d, cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(d.driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.name, err)
	}

	applyPool(db, d, cfg)

	// Sanity-check the connection before returning; cloud DBs commonly fail
	// here (auth, TLS, firewall) and a quick ping gives a much nicer error
	// than a deferred crash on the first query.
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", d.name, err)
	}

	s := &Store{db: db, dialect: d}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenSQLite is a convenience wrapper preserving the original single-argument
// API used by tests and the CLI when only a file path is supplied.
func OpenSQLite(path string) (*Store, error) {
	return Open(Config{Driver: "sqlite", Path: path})
}

// Driver returns the active dialect name (sqlite/mysql/postgres). Useful for
// logging and /api/runtime introspection.
func (s *Store) Driver() string {
	if s == nil {
		return ""
	}
	return s.dialect.name
}

// Close releases database resources.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying handle for advanced callers.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate() error {
	for _, stmt := range s.dialect.schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate (%s): %w", s.dialect.name, err)
		}
	}
	return nil
}

// LogCall persists a single call record. Safe for concurrent use; the
// underlying database/sql pool already serialises connection access and (for
// SQLite) we pin MaxOpenConns=1 in applyPool which makes writes inherently
// serial without us needing an application-level mutex.
func (s *Store) LogCall(ctx context.Context, r CallRecord) error {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		s.dialect.rebind(`INSERT INTO calls(ts, provider, model, path, status, latency_ms, bytes_in, bytes_out, tokens_in, tokens_out, client_ip, err)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`),
		r.Timestamp.UnixMilli(), r.Provider, r.Model, r.Path, r.Status, r.LatencyMS,
		r.BytesIn, r.BytesOut, r.TokensIn, r.TokensOut, r.ClientIP, r.ErrMessage,
	)
	return err
}

// DeleteCallsBefore removes call records older than the provided timestamp and
// returns the number of deleted rows. It is used by the retention job and is
// safe for all supported SQL dialects through placeholder rebinding.
func (s *Store) DeleteCallsBefore(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, s.dialect.rebind(`DELETE FROM calls WHERE ts < ?`), before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ApplyRetention deletes records older than the configured number of days. A
// non-positive day count disables retention and is treated as a no-op.
func (s *Store) ApplyRetention(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return s.DeleteCallsBefore(ctx, before)
}

// Summary aggregates traffic by provider over a time window.
type Summary struct {
	TotalCalls   int64                   `json:"total_calls"`
	TotalErrors  int64                   `json:"total_errors"`
	AvgLatencyMS float64                 `json:"avg_latency_ms"`
	TokensIn     int64                   `json:"tokens_in"`
	TokensOut    int64                   `json:"tokens_out"`
	WindowHours  int                     `json:"window_hours"`
	Providers    map[string]ProviderStat `json:"providers"`
}

// ProviderStat is the per-provider portion of Summary.
type ProviderStat struct {
	Calls        int64   `json:"calls"`
	Errors       int64   `json:"errors"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
}

// Summarize aggregates calls over the last `hours` hours.
func (s *Store) Summarize(ctx context.Context, hours int) (*Summary, error) {
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()

	row := s.db.QueryRowContext(ctx,
		s.dialect.rebind(`SELECT COUNT(1),
		        COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
		        COALESCE(AVG(latency_ms), 0),
		        COALESCE(SUM(tokens_in), 0),
		        COALESCE(SUM(tokens_out), 0)
		   FROM calls WHERE ts >= ?`), since)
	sum := &Summary{WindowHours: hours, Providers: map[string]ProviderStat{}}
	if err := row.Scan(&sum.TotalCalls, &sum.TotalErrors, &sum.AvgLatencyMS, &sum.TokensIn, &sum.TokensOut); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		s.dialect.rebind(`SELECT provider,
		        COUNT(1),
		        COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
		        COALESCE(AVG(latency_ms), 0),
		        COALESCE(SUM(tokens_in), 0),
		        COALESCE(SUM(tokens_out), 0)
		   FROM calls WHERE ts >= ?
		  GROUP BY provider`), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var ps ProviderStat
		if err := rows.Scan(&name, &ps.Calls, &ps.Errors, &ps.AvgLatencyMS, &ps.TokensIn, &ps.TokensOut); err != nil {
			return nil, err
		}
		sum.Providers[name] = ps
	}
	return sum, rows.Err()
}

// RecentCalls returns the most recent N call records (newest first).
func (s *Store) RecentCalls(ctx context.Context, limit int) ([]CallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		s.dialect.rebind(`SELECT id, ts, provider, model, path, status, latency_ms, bytes_in, bytes_out, tokens_in, tokens_out, COALESCE(client_ip,''), COALESCE(err,'')
		   FROM calls ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CallRecord
	for rows.Next() {
		var r CallRecord
		var tsMS int64
		if err := rows.Scan(&r.ID, &tsMS, &r.Provider, &r.Model, &r.Path, &r.Status, &r.LatencyMS,
			&r.BytesIn, &r.BytesOut, &r.TokensIn, &r.TokensOut, &r.ClientIP, &r.ErrMessage); err != nil {
			return nil, err
		}
		r.Timestamp = time.UnixMilli(tsMS)
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolveDSN derives the final connection string for the dialect. For sqlite
// it also ensures the parent directory exists so callers don't need to mkdir
// themselves, and it applies sensible PRAGMAs unless the user has already
// passed query parameters.
func resolveDSN(d dialect, cfg Config) (string, error) {
	switch d.name {
	case "sqlite":
		raw := strings.TrimSpace(cfg.DSN)
		if raw == "" {
			raw = strings.TrimSpace(cfg.Path)
		}
		if raw == "" {
			return "", errors.New("sqlite: empty db path (set DB_PATH or DB_DSN)")
		}
		// Pull off any pre-existing query string so we can mkdir the parent.
		path := raw
		query := ""
		if idx := strings.Index(raw, "?"); idx >= 0 {
			path = raw[:idx]
			query = raw[idx:]
		}
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("sqlite: create dir %s: %w", dir, err)
			}
		}
		if query == "" {
			query = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		}
		return path + query, nil
	case "mysql", "postgres":
		dsn := strings.TrimSpace(cfg.DSN)
		if dsn == "" {
			return "", fmt.Errorf("%s: DB_DSN is required for driver %q", d.name, d.name)
		}
		return dsn, nil
	default:
		return "", fmt.Errorf("unsupported db driver: %q", d.name)
	}
}

// applyPool configures the database/sql connection pool. SQLite gets a single
// writer connection (the journal mode is WAL but writers still serialise);
// MySQL/Postgres get pool defaults appropriate for cloud workloads.
func applyPool(db *sql.DB, d dialect, cfg Config) {
	switch d.name {
	case "sqlite":
		db.SetMaxOpenConns(1)
		if cfg.MaxOpenConns > 0 {
			db.SetMaxOpenConns(cfg.MaxOpenConns)
		}
		if cfg.MaxIdleConns > 0 {
			db.SetMaxIdleConns(cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime > 0 {
			db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
	default:
		max := cfg.MaxOpenConns
		if max <= 0 {
			max = 10
		}
		idle := cfg.MaxIdleConns
		if idle <= 0 {
			idle = 5
		}
		if idle > max {
			idle = max
		}
		lt := cfg.ConnMaxLifetime
		if lt <= 0 {
			lt = 30 * time.Minute
		}
		db.SetMaxOpenConns(max)
		db.SetMaxIdleConns(idle)
		db.SetConnMaxLifetime(lt)
	}
}
