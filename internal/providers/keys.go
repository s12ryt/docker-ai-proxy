package providers

import (
	"sync/atomic"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
)

// KeyPicker chooses an API key from a provider in a round-robin fashion.
type KeyPicker struct {
	cursor uint64
}

// Pick returns the next API key for the given provider, or empty string when none configured.
func (k *KeyPicker) Pick(p config.Provider) string {
	if len(p.APIKeys) == 0 {
		return ""
	}
	if len(p.APIKeys) == 1 {
		return p.APIKeys[0]
	}
	idx := atomic.AddUint64(&k.cursor, 1) - 1
	return p.APIKeys[int(idx%uint64(len(p.APIKeys)))]
}
