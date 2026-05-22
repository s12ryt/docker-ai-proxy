package providers

import (
	"testing"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
)

func TestKeyPicker_Empty(t *testing.T) {
	var k KeyPicker
	if got := k.Pick(config.Provider{Name: "x"}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestKeyPicker_RoundRobin(t *testing.T) {
	var k KeyPicker
	p := config.Provider{Name: "test", APIKeys: []string{"a", "b", "c"}}
	seq := []string{}
	for i := 0; i < 6; i++ {
		seq = append(seq, k.Pick(p))
	}
	want := []string{"a", "b", "c", "a", "b", "c"}
	for i, v := range want {
		if seq[i] != v {
			t.Fatalf("at %d want %q got %q (seq=%v)", i, v, seq[i], seq)
		}
	}
}

// TestKeyPicker_PerProviderIndependence verifies that traffic to one provider
// does not advance the cursor of another — a regression guard for the previous
// single shared cursor which would interleave rotations.
func TestKeyPicker_PerProviderIndependence(t *testing.T) {
	var k KeyPicker
	openai := config.Provider{Name: "openai", APIKeys: []string{"o1", "o2"}}
	gemini := config.Provider{Name: "gemini", APIKeys: []string{"g1", "g2", "g3"}}

	// Hammer openai a few times.
	for i := 0; i < 5; i++ {
		k.Pick(openai)
	}
	// Gemini should still start from its first key.
	if got := k.Pick(gemini); got != "g1" {
		t.Fatalf("gemini first pick: want %q got %q", "g1", got)
	}
	if got := k.Pick(gemini); got != "g2" {
		t.Fatalf("gemini second pick: want %q got %q", "g2", got)
	}
	// And openai's cursor is unaffected by gemini traffic.
	if got := k.Pick(openai); got != "o2" {
		t.Fatalf("openai 6th pick: want %q got %q", "o2", got)
	}
}
