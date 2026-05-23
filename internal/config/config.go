package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider describes an upstream LLM provider configuration.
type Provider struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	BaseURL     string   `json:"base_url"`
	APIKeys     []string `json:"api_keys"`
	Models      []string `json:"models"`
	Enabled     bool     `json:"enabled"`
	Weight      int      `json:"weight"`
	TimeoutSec  int      `json:"timeout_sec"`
}

// Config is the application configuration.
type Config struct {
	Listen          string     `json:"listen"`
	AdminToken      string     `json:"admin_token"`
	AccessTokens    []string   `json:"access_tokens"`
	TelegramUserID  string     `json:"telegram_user_id"`
	TelegramBotID   string     `json:"telegram_bot_id"`
	DBPath          string     `json:"db_path"`
	DBDriver        string     `json:"db_driver"`
	DBDSN           string     `json:"db_dsn"`
	DBMaxOpen       int        `json:"db_max_open_conns"`
	DBMaxIdle       int        `json:"db_max_idle_conns"`
	DBConnMaxLife   string     `json:"db_conn_max_lifetime"`
	DBRetentionDays int        `json:"db_retention_days"`
	AuthJWTSecret    string     `json:"auth_jwt_secret"`
	AuthCookieSecure bool      `json:"auth_cookie_secure"`
	AuthSessionTTL   string     `json:"auth_session_ttl"`
	Providers       []Provider `json:"providers"`
	EnableMetrics   bool       `json:"enable_metrics"`
	StartedAt       time.Time  `json:"-"`

	mu *sync.RWMutex
}

var (
	current *Config
	once    sync.Once
)

// Get returns the current config (thread-safe snapshot).
func Get() *Config {
	once.Do(func() {
		current = load()
	})
	return current
}

// Reload re-reads configuration from disk (if a config file is present).
func Reload() {
	c := load()
	if current == nil {
		current = c
		return
	}
	if current.mu == nil {
		current.mu = new(sync.RWMutex)
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	current.Listen = c.Listen
	current.AdminToken = c.AdminToken
	current.AccessTokens = c.AccessTokens
	current.TelegramUserID = c.TelegramUserID
	current.TelegramBotID = c.TelegramBotID
	current.DBPath = c.DBPath
	current.DBDriver = c.DBDriver
	current.DBDSN = c.DBDSN
	current.DBMaxOpen = c.DBMaxOpen
	current.DBMaxIdle = c.DBMaxIdle
	current.DBConnMaxLife = c.DBConnMaxLife
	current.DBRetentionDays = c.DBRetentionDays
	current.AuthJWTSecret = c.AuthJWTSecret
	current.AuthCookieSecure = c.AuthCookieSecure
	current.AuthSessionTTL = c.AuthSessionTTL
	current.Providers = c.Providers
	current.EnableMetrics = c.EnableMetrics
}

// Snapshot returns a deep copy safe for concurrent read-only use. Every
// nested slice (Providers + their APIKeys/Models) is duplicated so callers
// can read fields without any further synchronisation even while Reload()
// mutates the underlying config.
func (c *Config) Snapshot() Config {
	if c.mu != nil {
		c.mu.RLock()
		defer c.mu.RUnlock()
	}
	out := *c
	out.mu = nil
	out.AccessTokens = append([]string(nil), c.AccessTokens...)
	if len(c.Providers) > 0 {
		out.Providers = make([]Provider, len(c.Providers))
		for i, p := range c.Providers {
			cp := p
			cp.APIKeys = append([]string(nil), p.APIKeys...)
			cp.Models = append([]string(nil), p.Models...)
			out.Providers[i] = cp
		}
	} else {
		out.Providers = nil
	}
	return out
}

// ReplaceProviders swaps the provider list in memory after validation and normalization.
func (c *Config) ReplaceProviders(providers []Provider) error {
	if err := ValidateProviders(providers); err != nil {
		return err
	}
	NormalizeProviders(providers)
	if c.mu != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	c.Providers = cloneProviders(providers)
	return nil
}

// FindProvider locates a provider by name (case-insensitive).
func (c *Config) FindProvider(name string) (Provider, bool) {
	if c.mu != nil {
		c.mu.RLock()
		defer c.mu.RUnlock()
	}
	for _, p := range c.Providers {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Provider{}, false
}

// ProviderForModel maps a model identifier to a provider.
// Callers may use "providerName/model" or rely on registered models.
func (c *Config) ProviderForModel(model string) (Provider, string, error) {
	if c.mu != nil {
		c.mu.RLock()
		defer c.mu.RUnlock()
	}

	if idx := strings.Index(model, "/"); idx > 0 {
		prefix := model[:idx]
		rest := model[idx+1:]
		for _, p := range c.Providers {
			if !p.Enabled {
				continue
			}
			if strings.EqualFold(p.Name, prefix) {
				return p, rest, nil
			}
		}
	}

	for _, p := range c.Providers {
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m, model) {
				return p, model, nil
			}
		}
	}
	return Provider{}, "", errors.New("no provider available for model: " + model)
}

func load() *Config {
	c := &Config{
		mu:             new(sync.RWMutex),
		Listen:         ":8080",
		AdminToken:     "change-me-admin",
		DBPath:         "data/ai-hub.db",
		DBDriver:       "sqlite",
		AuthSessionTTL: "24h",
		EnableMetrics:  true,
		StartedAt:      time.Now(),
	}

	path := envOr("CONFIG_PATH", "config.json")
	if data, err := os.ReadFile(path); err == nil {
		var fileCfg Config
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			log.Printf("[config] parse %s failed: %v", path, err)
		} else {
			if fileCfg.Listen != "" {
				c.Listen = fileCfg.Listen
			}
			if fileCfg.AdminToken != "" {
				c.AdminToken = fileCfg.AdminToken
			}
			if len(fileCfg.AccessTokens) > 0 {
				c.AccessTokens = fileCfg.AccessTokens
			}
			if fileCfg.TelegramUserID != "" {
				c.TelegramUserID = fileCfg.TelegramUserID
			}
			if fileCfg.TelegramBotID != "" {
				c.TelegramBotID = fileCfg.TelegramBotID
			}
			if fileCfg.DBPath != "" {
				c.DBPath = fileCfg.DBPath
			}
			if fileCfg.DBDriver != "" {
				c.DBDriver = fileCfg.DBDriver
			}
			if fileCfg.DBDSN != "" {
				c.DBDSN = fileCfg.DBDSN
			}
			if fileCfg.DBMaxOpen != 0 {
				c.DBMaxOpen = fileCfg.DBMaxOpen
			}
			if fileCfg.DBMaxIdle != 0 {
				c.DBMaxIdle = fileCfg.DBMaxIdle
			}
			if fileCfg.DBConnMaxLife != "" {
				c.DBConnMaxLife = fileCfg.DBConnMaxLife
			}
			if fileCfg.DBRetentionDays > 0 {
				c.DBRetentionDays = fileCfg.DBRetentionDays
			}
			if fileCfg.AuthJWTSecret != "" {
				c.AuthJWTSecret = fileCfg.AuthJWTSecret
			}
			c.AuthCookieSecure = fileCfg.AuthCookieSecure
			if fileCfg.AuthSessionTTL != "" {
				c.AuthSessionTTL = fileCfg.AuthSessionTTL
			}
			if len(fileCfg.Providers) > 0 {
				c.Providers = fileCfg.Providers
			}
		}
	}

	applyEnvOverrides(c)

	if len(c.Providers) == 0 {
		c.Providers = defaultProviders()
	}

	NormalizeProviders(c.Providers)
	for i := range c.Providers {
		if c.Providers[i].TimeoutSec == 0 {
			c.Providers[i].TimeoutSec = 120
		}
		if c.Providers[i].Weight == 0 {
			c.Providers[i].Weight = 1
		}
	}
	return c
}

// ConfigPath returns the active configuration file path.
func ConfigPath() string {
	return envOr("CONFIG_PATH", "config.json")
}

// SaveProviders replaces the providers array in the active configuration file
// and updates the in-memory config. Environment variable overrides still take
// priority for other top-level settings when the process reloads.
func SaveProviders(providers []Provider) error {
	if err := ValidateProviders(providers); err != nil {
		return err
	}
	NormalizeProviders(providers)

	path := ConfigPath()
	fileCfg := Config{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	fileCfg.Providers = providers

	data, err := json.MarshalIndent(fileCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if current == nil {
		current = load()
		return nil
	}
	if current.mu == nil {
		current.mu = new(sync.RWMutex)
	}
	current.mu.Lock()
	current.Providers = cloneProviders(providers)
	current.mu.Unlock()
	return nil
}

// ValidateProviders checks the user-editable provider list before persisting it.
func ValidateProviders(providers []Provider) error {
	seen := map[string]struct{}{}
	for i, p := range providers {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("provider #%d name is required", i+1)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("provider %q is duplicated", name)
		}
		seen[key] = struct{}{}
		if strings.ContainsAny(name, "/ \\\t\r\n") {
			return fmt.Errorf("provider %q name cannot contain spaces or slashes", name)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("provider %q base_url is required", name)
		}
	}
	return nil
}

// NormalizeProviders trims whitespace and applies runtime defaults to providers.
func NormalizeProviders(providers []Provider) {
	for i := range providers {
		providers[i].Name = strings.TrimSpace(providers[i].Name)
		providers[i].DisplayName = strings.TrimSpace(providers[i].DisplayName)
		providers[i].BaseURL = strings.TrimRight(strings.TrimSpace(providers[i].BaseURL), "/")
		providers[i].APIKeys = cleanStrings(providers[i].APIKeys)
		providers[i].Models = cleanStrings(providers[i].Models)
		if providers[i].Weight == 0 {
			providers[i].Weight = 1
		}
		if providers[i].TimeoutSec == 0 {
			providers[i].TimeoutSec = 120
		}
	}
}

func cloneProviders(providers []Provider) []Provider {
	out := make([]Provider, len(providers))
	for i, p := range providers {
		out[i] = p
		out[i].APIKeys = append([]string(nil), p.APIKeys...)
		out[i].Models = append([]string(nil), p.Models...)
	}
	return out
}

func cleanStrings(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func applyEnvOverrides(c *Config) {
	if v := os.Getenv("LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		c.AdminToken = v
	}
	if tokens := os.Getenv("ACCESS_TOKENS"); tokens != "" {
		c.AccessTokens = splitCSV(tokens)
	}
	if v := os.Getenv("TELEGRAM_USER_ID"); v != "" {
		c.TelegramUserID = v
	}
	if v := os.Getenv("TELEGRAM_BOT_ID"); v != "" {
		c.TelegramBotID = v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("DB_DRIVER"); v != "" {
		c.DBDriver = v
	}
	if v := os.Getenv("DB_DSN"); v != "" {
		c.DBDSN = v
	}
	if v := envInt("DB_MAX_OPEN_CONNS", c.DBMaxOpen); v != c.DBMaxOpen {
		c.DBMaxOpen = v
	}
	if v := envInt("DB_MAX_IDLE_CONNS", c.DBMaxIdle); v != c.DBMaxIdle {
		c.DBMaxIdle = v
	}
	if v := os.Getenv("DB_CONN_MAX_LIFETIME"); v != "" {
		c.DBConnMaxLife = v
	}
	if v := envInt("DB_RETENTION_DAYS", c.DBRetentionDays); v != c.DBRetentionDays {
		c.DBRetentionDays = v
	}
	if v := os.Getenv("AUTH_JWT_SECRET"); v != "" {
		c.AuthJWTSecret = v
	}
	if v := os.Getenv("AUTH_COOKIE_SECURE"); v != "" {
		c.AuthCookieSecure = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("AUTH_SESSION_TTL"); v != "" {
		c.AuthSessionTTL = v
	}
	if v := os.Getenv("ENABLE_METRICS"); v != "" {
		c.EnableMetrics = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
}

// AuthSessionDuration returns the configured login session lifetime.
func (c *Config) AuthSessionDuration() time.Duration {
	if c == nil || strings.TrimSpace(c.AuthSessionTTL) == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.AuthSessionTTL))
	if err != nil || d <= 0 {
		log.Printf("[config] auth_session_ttl=%q invalid, using 24h", c.AuthSessionTTL)
		return 24 * time.Hour
	}
	return d
}

func splitCSV(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// envInt reads a positive integer from the named environment variable. Missing
// or unparseable values return fallback so callers can apply driver-specific
// defaults afterwards.
func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("[config] %s=%q invalid, ignored", key, v)
		return fallback
	}
	return n
}

func defaultProviders() []Provider {
	return []Provider{
		{
			Name:        "openai",
			DisplayName: "OpenAI",
			BaseURL:     "https://api.openai.com",
			Models:      []string{"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-3.5-turbo"},
			Enabled:     false,
			Weight:      1,
			TimeoutSec:  120,
		},
		{
			Name:        "anthropic",
			DisplayName: "Anthropic",
			BaseURL:     "https://api.anthropic.com",
			Models:      []string{"claude-opus-4", "claude-sonnet-4", "claude-3-5-sonnet"},
			Enabled:     false,
			Weight:      1,
			TimeoutSec:  120,
		},
		{
			Name:        "gemini",
			DisplayName: "Google Gemini",
			BaseURL:     "https://generativelanguage.googleapis.com",
			Models:      []string{"gemini-1.5-pro", "gemini-1.5-flash", "gemini-2.0-flash"},
			Enabled:     false,
			Weight:      1,
			TimeoutSec:  120,
		},
		{
			Name:        "deepseek",
			DisplayName: "DeepSeek",
			BaseURL:     "https://api.deepseek.com",
			Models:      []string{"deepseek-chat", "deepseek-reasoner"},
			Enabled:     false,
			Weight:      1,
			TimeoutSec:  120,
		},
	}
}
