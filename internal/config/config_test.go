package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderForModel_Direct(t *testing.T) {
	c := &Config{Providers: []Provider{
		{Name: "openai", Enabled: true, Models: []string{"gpt-4o-mini"}},
		{Name: "anthropic", Enabled: true, Models: []string{"claude-3-5-sonnet"}},
	}}
	p, m, err := c.ProviderForModel("gpt-4o-mini")
	if err != nil {
		t.Fatalf("expected match, got %v", err)
	}
	if p.Name != "openai" || m != "gpt-4o-mini" {
		t.Fatalf("wrong route: %s / %s", p.Name, m)
	}
}

func TestProviderForModel_Prefix(t *testing.T) {
	c := &Config{Providers: []Provider{
		{Name: "deepseek", Enabled: true, Models: []string{"deepseek-chat"}},
	}}
	p, m, err := c.ProviderForModel("deepseek/my-custom")
	if err != nil {
		t.Fatalf("expected prefix match, got %v", err)
	}
	if p.Name != "deepseek" || m != "my-custom" {
		t.Fatalf("wrong route: %s / %s", p.Name, m)
	}
}

func TestProviderForModel_Disabled(t *testing.T) {
	c := &Config{Providers: []Provider{
		{Name: "openai", Enabled: false, Models: []string{"gpt-4o-mini"}},
	}}
	if _, _, err := c.ProviderForModel("gpt-4o-mini"); err == nil {
		t.Fatal("expected error for disabled provider")
	}
}

func TestProviderForModel_Unknown(t *testing.T) {
	c := &Config{Providers: []Provider{
		{Name: "openai", Enabled: true, Models: []string{"gpt-4o-mini"}},
	}}
	if _, _, err := c.ProviderForModel("totally-unknown"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindProvider(t *testing.T) {
	c := &Config{Providers: []Provider{{Name: "OpenAI"}}}
	if _, ok := c.FindProvider("openai"); !ok {
		t.Fatal("case-insensitive lookup failed")
	}
	if _, ok := c.FindProvider("nope"); ok {
		t.Fatal("expected miss")
	}
}

func TestLoadReadsTelegramConfigFromJSON(t *testing.T) {
	path := writeTempConfig(t, `{
		"admin_token": "from-file",
		"telegram_user_id": "user-from-file",
		"telegram_bot_id": "bot-from-file",
		"providers": [
			{"name": "openai", "enabled": true, "models": ["gpt-4o-mini"]}
		]
	}`)
	t.Setenv("CONFIG_PATH", path)
	t.Setenv("TELEGRAM_USER_ID", "")
	t.Setenv("TELEGRAM_BOT_ID", "")

	c := load()
	if c.TelegramUserID != "user-from-file" {
		t.Fatalf("telegram user id not loaded: %q", c.TelegramUserID)
	}
	if c.TelegramBotID != "bot-from-file" {
		t.Fatalf("telegram bot id not loaded: %q", c.TelegramBotID)
	}
}

func TestLoadEnvOverridesTelegramConfig(t *testing.T) {
	path := writeTempConfig(t, `{
		"telegram_user_id": "user-from-file",
		"telegram_bot_id": "bot-from-file"
	}`)
	t.Setenv("CONFIG_PATH", path)
	t.Setenv("TELEGRAM_USER_ID", "user-from-env")
	t.Setenv("TELEGRAM_BOT_ID", "bot-from-env")

	c := load()
	if c.TelegramUserID != "user-from-env" {
		t.Fatalf("telegram user id should prefer env, got %q", c.TelegramUserID)
	}
	if c.TelegramBotID != "bot-from-env" {
		t.Fatalf("telegram bot id should prefer env, got %q", c.TelegramBotID)
	}
}

func TestLoadEnvOverridesFileConfig(t *testing.T) {
	path := writeTempConfig(t, `{
		"admin_token": "from-file",
		"db_driver": "sqlite",
		"db_dsn": "file-dsn",
		"db_max_open_conns": 2,
		"db_max_idle_conns": 1,
		"db_conn_max_lifetime": "5m",
		"enable_metrics": true
	}`)
	t.Setenv("CONFIG_PATH", path)
	t.Setenv("ADMIN_TOKEN", "from-env")
	t.Setenv("DB_DRIVER", "postgres")
	t.Setenv("DB_DSN", "postgres://user:pass@example.test/db?sslmode=require")
	t.Setenv("DB_MAX_OPEN_CONNS", "17")
	t.Setenv("DB_MAX_IDLE_CONNS", "9")
	t.Setenv("DB_CONN_MAX_LIFETIME", "45m")
	t.Setenv("ENABLE_METRICS", "false")

	c := load()
	if c.AdminToken != "from-env" {
		t.Fatalf("admin token should prefer env, got %q", c.AdminToken)
	}
	if c.DBDriver != "postgres" {
		t.Fatalf("db driver should prefer env, got %q", c.DBDriver)
	}
	if c.DBDSN != "postgres://user:pass@example.test/db?sslmode=require" {
		t.Fatalf("db dsn should prefer env, got %q", c.DBDSN)
	}
	if c.DBMaxOpen != 17 {
		t.Fatalf("db max open should prefer env, got %d", c.DBMaxOpen)
	}
	if c.DBMaxIdle != 9 {
		t.Fatalf("db max idle should prefer env, got %d", c.DBMaxIdle)
	}
	if c.DBConnMaxLife != "45m" {
		t.Fatalf("db conn max lifetime should prefer env, got %q", c.DBConnMaxLife)
	}
	if c.EnableMetrics {
		t.Fatal("enable metrics should prefer env false")
	}
}

func TestLoadEnvAccessTokensSplitCSV(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("ACCESS_TOKENS", " first,second ,, third ")

	c := load()
	want := []string{"first", "second", "third"}
	if len(c.AccessTokens) != len(want) {
		t.Fatalf("wrong token count: got %#v", c.AccessTokens)
	}
	for i := range want {
		if c.AccessTokens[i] != want[i] {
			t.Fatalf("token %d mismatch: got %#v want %#v", i, c.AccessTokens, want)
		}
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}
