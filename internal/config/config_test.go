package config

import "testing"

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
