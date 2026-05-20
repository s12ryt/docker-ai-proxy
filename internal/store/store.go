package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database used for request logs and aggregated metrics.
type Store struct {
	db *sql.DB
	mu sync.Mutex
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

// Open initialises the SQLite store and applies the schema.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("empty db path")
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite serialises writes; keep it simple.
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
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
	const schema = `
CREATE TABLE IF NOT EXISTS calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    path TEXT NOT NULL,
    status INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL,
    bytes_in INTEGER NOT NULL DEFAULT 0,
    bytes_out INTEGER NOT NULL DEFAULT 0,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    client_ip TEXT,
    err TEXT
);
CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);
CREATE INDEX IF NOT EXISTS idx_calls_provider_ts ON calls(provider, ts);
`
	_, err := s.db.Exec(schema)
	return err
}

// LogCall persists a single call record.
func (s *Store) LogCall(ctx context.Context, r CallRecord) error {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO calls(ts, provider, model, path, status, latency_ms, bytes_in, bytes_out, tokens_in, tokens_out, client_ip, err)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Timestamp.UnixMilli(), r.Provider, r.Model, r.Path, r.Status, r.LatencyMS,
		r.BytesIn, r.BytesOut, r.TokensIn, r.TokensOut, r.ClientIP, r.ErrMessage,
	)
	return err
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
		`SELECT COUNT(1),
		        COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
		        COALESCE(AVG(latency_ms), 0),
		        COALESCE(SUM(tokens_in), 0),
		        COALESCE(SUM(tokens_out), 0)
		   FROM calls WHERE ts >= ?`, since)
	sum := &Summary{WindowHours: hours, Providers: map[string]ProviderStat{}}
	if err := row.Scan(&sum.TotalCalls, &sum.TotalErrors, &sum.AvgLatencyMS, &sum.TokensIn, &sum.TokensOut); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT provider,
		        COUNT(1),
		        SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
		        COALESCE(AVG(latency_ms), 0),
		        COALESCE(SUM(tokens_in), 0),
		        COALESCE(SUM(tokens_out), 0)
		   FROM calls WHERE ts >= ?
		  GROUP BY provider`, since)
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
		`SELECT id, ts, provider, model, path, status, latency_ms, bytes_in, bytes_out, tokens_in, tokens_out, COALESCE(client_ip,''), COALESCE(err,'')
		   FROM calls ORDER BY id DESC LIMIT ?`, limit)
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
