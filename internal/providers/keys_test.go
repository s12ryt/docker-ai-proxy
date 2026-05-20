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
	p := config.Provider{APIKeys: []string{"a", "b", "c"}}
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
