package proxy

import (
	"fmt"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/config"
	"github.com/s12ryt/docker-ai-proxy/internal/protocol"
)

// providerKind classifies a provider by its *native* wire protocol so we know
// which encoder/decoder pair to apply. Multiple providers can share a kind
// (e.g. openai and deepseek both speak OpenAI).
type providerKind int

const (
	kindOpenAI providerKind = iota
	kindAnthropic
	kindGemini
)

func (k providerKind) String() string {
	switch k {
	case kindAnthropic:
		return "anthropic"
	case kindGemini:
		return "gemini"
	default:
		return "openai"
	}
}

// providerKindOf maps a configured provider to its wire protocol.
//
// Heuristics, in priority order:
//  1. Provider.Name matches a well-known vendor (anthropic / gemini / google).
//  2. Provider.BaseURL host hints at a vendor (api.anthropic.com /
//     generativelanguage.googleapis.com).
//  3. Otherwise treat as OpenAI-compatible (openai, deepseek, groq, together,
//     fireworks, mistral, perplexity, custom OAI-compatible gateways …).
func providerKindOf(p config.Provider) providerKind {
	name := strings.ToLower(strings.TrimSpace(p.Name))
	switch name {
	case "anthropic", "claude":
		return kindAnthropic
	case "gemini", "google", "googleai", "vertex":
		return kindGemini
	}
	base := strings.ToLower(p.BaseURL)
	switch {
	case strings.Contains(base, "anthropic.com"):
		return kindAnthropic
	case strings.Contains(base, "generativelanguage.googleapis.com"),
		strings.Contains(base, "aiplatform.googleapis.com"):
		return kindGemini
	}
	return kindOpenAI
}

// upstreamPathForChat returns the path component of the upstream chat
// completion endpoint for the given kind. The model name is required for
// Gemini because it embeds the model in the URL itself.
func upstreamPathForChat(kind providerKind, model string, stream bool) string {
	switch kind {
	case kindAnthropic:
		return "/v1/messages"
	case kindGemini:
		op := "generateContent"
		if stream {
			op = "streamGenerateContent"
		}
		// Gemini's REST surface is /v1beta/models/{model}:{op}. The
		// model segment must be URL-safe; we leave URL-escaping to the
		// caller (model names are alphanumeric + "-" / "." in practice).
		return fmt.Sprintf("/v1beta/models/%s:%s", model, op)
	default:
		return "/v1/chat/completions"
	}
}

// translateChatRequest converts an OpenAI-format `/v1/chat/completions`
// request body into the wire format expected by `dst`. When `dst` is
// kindOpenAI the body is returned unchanged (after model rewrite).
//
// The returned body is ready to ship verbatim to the upstream provider.
func translateChatRequest(srcBody []byte, dst providerKind, upstreamModel string) ([]byte, protocol.ChatRequest, error) {
	ir, err := protocol.DecodeOpenAIChat(srcBody)
	if err != nil {
		return nil, protocol.ChatRequest{}, fmt.Errorf("decode openai request: %w", err)
	}
	// Always replace the model with the provider-native form before
	// encoding back out — the caller has already resolved aliases.
	ir.Model = upstreamModel

	switch dst {
	case kindAnthropic:
		out, err := protocol.EncodeAnthropicChat(ir)
		if err != nil {
			return nil, ir, fmt.Errorf("encode anthropic request: %w", err)
		}
		return out, ir, nil
	case kindGemini:
		out, err := protocol.EncodeGeminiChat(ir)
		if err != nil {
			return nil, ir, fmt.Errorf("encode gemini request: %w", err)
		}
		return out, ir, nil
	default:
		// OpenAI / DeepSeek / any OAI-compatible upstream: re-emit the
		// IR through the OpenAI encoder so we end up with a canonical
		// (and validated) payload rather than blindly forwarding raw
		// JSON. The fast pass-through path remains available via
		// translateChatRequestPassThrough for callers that want to
		// preserve byte-identical bodies.
		out, err := protocol.EncodeOpenAIChat(ir)
		if err != nil {
			return nil, ir, fmt.Errorf("encode openai request: %w", err)
		}
		return out, ir, nil
	}
}

// translateChatResponse converts a non-stream upstream chat response into
// OpenAI `/v1/chat/completions` format. When `src` is kindOpenAI the body is
// returned unchanged.
//
// `requestModel` is the model the *client* originally asked for; we echo it
// back so clients see a stable identifier even when the upstream returns a
// different model id (Anthropic does this routinely).
func translateChatResponse(src providerKind, requestModel string, upstreamBody []byte) ([]byte, error) {
	switch src {
	case kindAnthropic:
		ir, err := protocol.DecodeAnthropicResponse(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("decode anthropic response: %w", err)
		}
		fillDefaultsForOpenAIResponse(&ir, requestModel)
		return protocol.EncodeOpenAIResponse(ir)
	case kindGemini:
		ir, err := protocol.DecodeGeminiResponse(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("decode gemini response: %w", err)
		}
		fillDefaultsForOpenAIResponse(&ir, requestModel)
		return protocol.EncodeOpenAIResponse(ir)
	default:
		// Pass through unchanged. If the upstream emitted invalid JSON
		// we still forward it so the client sees the original error.
		return upstreamBody, nil
	}
}

// fillDefaultsForOpenAIResponse populates fields that OpenAI clients expect
// to always be present but vendor responses may omit.
func fillDefaultsForOpenAIResponse(ir *protocol.ChatResponse, requestModel string) {
	if ir.ID == "" {
		ir.ID = "chatcmpl-" + randomID(24)
	}
	if ir.Created == 0 {
		ir.Created = time.Now().Unix()
	}
	if ir.Model == "" {
		ir.Model = requestModel
	}
}

// randomID returns a short hex-ish identifier suitable for synthetic
// chat-completion IDs. We avoid pulling in crypto/rand for what is purely a
// debugging aid; the timestamp guarantees uniqueness in practice.
func randomID(n int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	now := time.Now().UnixNano()
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		idx := int(uint64(now>>uint(i*5)) % uint64(len(alphabet)))
		b.WriteByte(alphabet[idx])
	}
	return b.String()
}
