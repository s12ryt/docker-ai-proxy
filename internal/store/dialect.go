package store

import (
	"fmt"
	"strings"
)

// dialect captures driver-specific SQL flavour differences so the rest of the
// store can stay written against a single canonical query (using `?`
// placeholders and ANSI identifiers).
type dialect struct {
	// name is the driver name returned to callers (sqlite/mysql/postgres).
	name string
	// driverName is the registered database/sql driver name to pass to sql.Open.
	driverName string
	// schema returns the full CREATE TABLE / CREATE INDEX statements for this
	// dialect. Each statement is terminated with `;` and may be executed
	// individually for drivers that don't accept multi-statement scripts.
	schema []string
	// rebind transforms a query written with `?` placeholders into the dialect's
	// native placeholder syntax. For sqlite/mysql this is identity; for
	// postgres it rewrites to $1, $2, ...
	rebind func(string) string
}

func sqliteDialect() dialect {
	return dialect{
		name:       "sqlite",
		driverName: "sqlite",
		schema: []string{
			`CREATE TABLE IF NOT EXISTS bootstrap_state (
    state_key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    created_at INTEGER NOT NULL
);`,
			`CREATE TABLE IF NOT EXISTS calls (
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
    client_name TEXT,
    err TEXT
);`,
			`CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    disabled INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    last_login_at INTEGER
);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_provider_ts ON calls(provider, ts);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_client_ts ON calls(client_name, ts);`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);`,
		},
		rebind: func(q string) string { return q },
	}
}

func mysqlDialect() dialect {
	return dialect{
		name:       "mysql",
		driverName: "mysql",
		schema: []string{
			`CREATE TABLE IF NOT EXISTS bootstrap_state (
    state_key VARCHAR(128) NOT NULL,
    value TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (state_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,
			`CREATE TABLE IF NOT EXISTS calls (
    id BIGINT NOT NULL AUTO_INCREMENT,
    ts BIGINT NOT NULL,
    provider VARCHAR(128) NOT NULL,
    model VARCHAR(255) NOT NULL,
    path VARCHAR(512) NOT NULL,
    status INT NOT NULL,
    latency_ms BIGINT NOT NULL,
    bytes_in BIGINT NOT NULL DEFAULT 0,
    bytes_out BIGINT NOT NULL DEFAULT 0,
    tokens_in BIGINT NOT NULL DEFAULT 0,
    tokens_out BIGINT NOT NULL DEFAULT 0,
    client_ip VARCHAR(64) NULL,
    client_name VARCHAR(128) NULL,
    err TEXT NULL,
    PRIMARY KEY (id),
    KEY idx_calls_ts (ts),
    KEY idx_calls_provider_ts (provider, ts),
    KEY idx_calls_client_ts (client_name, ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,
			`CREATE TABLE IF NOT EXISTS users (
    id BIGINT NOT NULL AUTO_INCREMENT,
    username VARCHAR(64) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'user',
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    last_login_at BIGINT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,
		},
		rebind: func(q string) string { return q },
	}
}

func postgresDialect() dialect {
	return dialect{
		name:       "postgres",
		driverName: "pgx",
		schema: []string{
			`CREATE TABLE IF NOT EXISTS bootstrap_state (
    state_key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    created_at BIGINT NOT NULL
);`,
			`CREATE TABLE IF NOT EXISTS calls (
    id BIGSERIAL PRIMARY KEY,
    ts BIGINT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    path TEXT NOT NULL,
    status INTEGER NOT NULL,
    latency_ms BIGINT NOT NULL,
    bytes_in BIGINT NOT NULL DEFAULT 0,
    bytes_out BIGINT NOT NULL DEFAULT 0,
    tokens_in BIGINT NOT NULL DEFAULT 0,
    tokens_out BIGINT NOT NULL DEFAULT 0,
    client_ip TEXT,
    client_name TEXT,
    err TEXT
);`,
			`CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    last_login_at BIGINT
);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_provider_ts ON calls(provider, ts);`,
			`CREATE INDEX IF NOT EXISTS idx_calls_client_ts ON calls(client_name, ts);`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);`,
		},
		rebind: rebindPostgres,
	}
}

// rebindPostgres converts `?` placeholders into `$N`, ignoring `?` inside
// single-quoted strings and SQL comments (`-- ...` / `/* ... */`).
func rebindPostgres(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	inQuote := false
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case inQuote:
			if c == '\'' && i+1 < len(q) && q[i+1] == '\'' {
				b.WriteByte('\'')
				b.WriteByte('\'')
				i++
				continue
			}
			if c == '\'' {
				inQuote = false
			}
			b.WriteByte(c)
		case c == '\'':
			inQuote = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			b.WriteByte(c)
			i++
			b.WriteByte(q[i])
			for i+1 < len(q) {
				i++
				b.WriteByte(q[i])
				if q[i] == '\n' {
					break
				}
			}
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			b.WriteByte(c)
			i++
			b.WriteByte(q[i])
			for i+1 < len(q) {
				i++
				b.WriteByte(q[i])
				if q[i] == '*' && i+1 < len(q) && q[i+1] == '/' {
					i++
					b.WriteByte(q[i])
					break
				}
			}
		case c == '?':
			n++
			fmt.Fprintf(&b, "$%d", n)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// resolveDialect chooses a dialect from a driver name (case-insensitive). The
// empty string and "sqlite3" are normalised to sqlite; "pg" and "postgresql"
// to postgres; "mariadb" to mysql.
func resolveDialect(name string) (dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sqlite", "sqlite3":
		return sqliteDialect(), nil
	case "mysql", "mariadb":
		return mysqlDialect(), nil
	case "postgres", "postgresql", "pg", "pgx":
		return postgresDialect(), nil
	default:
		return dialect{}, fmt.Errorf("unsupported db driver: %q (want sqlite|mysql|postgres)", name)
	}
}
