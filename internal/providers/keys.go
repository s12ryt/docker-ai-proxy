package providers

import (
	"sync"
	"sync/atomic"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
)

// KeyPicker chooses an API key from a provider in a round-robin fashion.
//
// Each provider gets its own cursor so concurrent traffic to different
// upstreams cannot perturb each other's rotation. Cursors are created on
// demand the first time a provider is seen.
type KeyPicker struct {
	mu      sync.RWMutex
	cursors map[string]*uint64
}

// Pick returns the next API key for the given provider, or empty string when none configured.
func (k *KeyPicker) Pick(p config.Provider) string {
	switch len(p.APIKeys) {
	case 0:
		return ""
	case 1:
		return p.APIKeys[0]
	}

	cursor := k.cursorFor(p.Name)
	// AddUint64 returns the new value; subtract 1 so the first call uses index 0.
	idx := atomic.AddUint64(cursor, 1) - 1
	return p.APIKeys[int(idx%uint64(len(p.APIKeys)))]
}

// cursorFor returns the rotation cursor for the given provider name, creating
// it the first time it's requested. Uses a double-checked pattern to keep the
// hot path lock-free after the first hit.
func (k *KeyPicker) cursorFor(name string) *uint64 {
	k.mu.RLock()
	c, ok := k.cursors[name]
	k.mu.RUnlock()
	if ok {
		return c
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if c, ok = k.cursors[name]; ok {
		return c
	}
	if k.cursors == nil {
		k.cursors = make(map[string]*uint64)
	}
	c = new(uint64)
	k.cursors[name] = c
	return c
}
