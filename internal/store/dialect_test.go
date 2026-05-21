package store

import (
	"context"
	"strings"
	"testing"
)

func TestResolveDialect(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"", "sqlite", false},
		{"sqlite", "sqlite", false},
		{"SQLite3", "sqlite", false},
		{"mysql", "mysql", false},
		{"MariaDB", "mysql", false},
		{"postgres", "postgres", false},
		{"postgresql", "postgres", false},
		{"pgx", "postgres", false},
		{"oracle", "", true},
	}
	for _, c := range cases {
		got, err := resolveDialect(c.in)
		if c.err {
			if err == nil {
				t.Errorf("resolveDialect(%q): want error, got %q", c.in, got.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveDialect(%q): unexpected err %v", c.in, err)
			continue
		}
		if got.name != c.want {
			t.Errorf("resolveDialect(%q): want %q, got %q", c.in, c.want, got.name)
		}
	}
}

func TestRebindPostgres(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SELECT 1", "SELECT 1"},
		{"WHERE a = ?", "WHERE a = $1"},
		{"WHERE a = ? AND b = ?", "WHERE a = $1 AND b = $2"},
		{
			"INSERT INTO t(a,b,c) VALUES (?,?,?)",
			"INSERT INTO t(a,b,c) VALUES ($1,$2,$3)",
		},
		// `?` inside a string literal must be preserved untouched.
		{"WHERE label = 'why?' AND id = ?", "WHERE label = 'why?' AND id = $1"},
		// Escaped `''` inside string literals must not toggle the quote state.
		{"WHERE x = 'it''s ok?' AND y = ?", "WHERE x = 'it''s ok?' AND y = $1"},
	}
	for _, c := range cases {
		got := rebindPostgres(c.in)
		if got != c.want {
			t.Errorf("rebindPostgres(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSQLiteDialectRebindIsIdentity(t *testing.T) {
	d := sqliteDialect()
	in := "SELECT * FROM t WHERE a = ? AND b = ?"
	if got := d.rebind(in); got != in {
		t.Fatalf("sqlite rebind should be identity, got %q", got)
	}
}

func TestResolveDSN_SQLiteDefaults(t *testing.T) {
	d := sqliteDialect()
	tmp := t.TempDir() + "/sub/ai.db"
	dsn, err := resolveDSN(d, Config{Driver: "sqlite", Path: tmp})
	if err != nil {
		t.Fatalf("resolveDSN err: %v", err)
	}
	if !strings.HasPrefix(dsn, tmp+"?") {
		t.Fatalf("expected DSN to start with path+? got %q", dsn)
	}
	if !strings.Contains(dsn, "journal_mode(WAL)") {
		t.Fatalf("expected default PRAGMA in DSN, got %q", dsn)
	}
}

func TestResolveDSN_CloudRequiresDSN(t *testing.T) {
	mysql, _ := resolveDialect("mysql")
	if _, err := resolveDSN(mysql, Config{Driver: "mysql"}); err == nil {
		t.Fatal("mysql with empty DSN should fail")
	}
	pg, _ := resolveDialect("postgres")
	if _, err := resolveDSN(pg, Config{Driver: "postgres"}); err == nil {
		t.Fatal("postgres with empty DSN should fail")
	}
}

func TestResolveDSN_SQLiteHonoursExistingQuery(t *testing.T) {
	d := sqliteDialect()
	in := t.TempDir() + "/a.db?_busy_timeout=1000"
	dsn, err := resolveDSN(d, Config{Driver: "sqlite", DSN: in})
	if err != nil {
		t.Fatalf("resolveDSN err: %v", err)
	}
	if !strings.HasSuffix(dsn, "?_busy_timeout=1000") {
		t.Fatalf("custom query should be preserved, got %q", dsn)
	}
}

func TestOpenSQLite_DriverName(t *testing.T) {
	st, err := OpenSQLite(t.TempDir() + "/p.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer st.Close()
	if err := st.db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if st.Driver() != "sqlite" {
		t.Fatalf("driver = %s, want sqlite", st.Driver())
	}
}
