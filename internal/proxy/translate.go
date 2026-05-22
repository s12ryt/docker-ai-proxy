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
// request body into the wire format expected by `dst`. It is kept for the
// Stage 2 OpenAI-in path; Stage 3 native routes use translateChatRequestFrom.
func translateChatRequest(srcBody []byte, dst providerKind, upstreamModel string) ([]byte, protocol.ChatRequest, error) {
	return translateChatRequestFrom(srcBody, kindOpenAI, dst, upstreamModel)
}

// translateChatRequestFrom converts a chat request from any supported inbound
// wire protocol into the upstream provider's native wire format.
func translateChatRequestFrom(srcBody []byte, src, dst providerKind, upstreamModel string) ([]byte, protocol.ChatRequest, error) {
	return translateChatRequestFromWithStream(srcBody, src, dst, upstreamModel, nil)
}

func translateChatRequestFromWithStream(srcBody []byte, src, dst providerKind, upstreamModel string, stream *bool) ([]byte, protocol.ChatRequest, error) {
	ir, err := decodeChatRequest(srcBody, src)
	if err != nil {
		return nil, protocol.ChatRequest{}, err
	}
	// Always replace the model with the provider-native form before encoding
	// back out — the caller has already resolved aliases.
	ir.Model = upstreamModel
	if stream != nil {
		ir.Stream = *stream
	}

	out, err := encodeChatRequest(ir, dst)
	if err != nil {
		return nil, ir, err
	}
	return out, ir, nil
}

func decodeChatRequest(body []byte, kind providerKind) (protocol.ChatRequest, error) {
	switch kind {
	case kindAnthropic:
		ir, err := protocol.DecodeAnthropicChat(body)
		if err != nil {
			return protocol.ChatRequest{}, fmt.Errorf("decode anthropic request: %w", err)
		}
		return ir, nil
	case kindGemini:
		ir, err := protocol.DecodeGeminiChat(body)
		if err != nil {
			return protocol.ChatRequest{}, fmt.Errorf("decode gemini request: %w", err)
		}
		return ir, nil
	default:
		ir, err := protocol.DecodeOpenAIChat(body)
		if err != nil {
			return protocol.ChatRequest{}, fmt.Errorf("decode openai request: %w", err)
		}
		return ir, nil
	}
}

func encodeChatRequest(ir protocol.ChatRequest, kind providerKind) ([]byte, error) {
	switch kind {
	case kindAnthropic:
		out, err := protocol.EncodeAnthropicChat(ir)
		if err != nil {
			return nil, fmt.Errorf("encode anthropic request: %w", err)
		}
		return out, nil
	case kindGemini:
		out, err := protocol.EncodeGeminiChat(ir)
		if err != nil {
			return nil, fmt.Errorf("encode gemini request: %w", err)
		}
		return out, nil
	default:
		out, err := protocol.EncodeOpenAIChat(ir)
		if err != nil {
			return nil, fmt.Errorf("encode openai request: %w", err)
		}
		return out, nil
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
	return translateChatResponseTo(src, kindOpenAI, requestModel, upstreamBody)
}

// translateChatResponseTo converts an upstream non-stream chat response from
// src protocol into the client-facing dst protocol.
func translateChatResponseTo(src, dst providerKind, requestModel string, upstreamBody []byte) ([]byte, error) {
	if src == dst {
		// Pass through unchanged. If the upstream emitted invalid JSON we still
		// forward it so the client sees the original response.
		return upstreamBody, nil
	}

	ir, err := decodeChatResponse(upstreamBody, src)
	if err != nil {
		return nil, err
	}
	return encodeChatResponse(ir, dst, requestModel)
}

func decodeChatResponse(body []byte, kind providerKind) (protocol.ChatResponse, error) {
	switch kind {
	case kindAnthropic:
		ir, err := protocol.DecodeAnthropicResponse(body)
		if err != nil {
			return protocol.ChatResponse{}, fmt.Errorf("decode anthropic response: %w", err)
		}
		return ir, nil
	case kindGemini:
		ir, err := protocol.DecodeGeminiResponse(body)
		if err != nil {
			return protocol.ChatResponse{}, fmt.Errorf("decode gemini response: %w", err)
		}
		return ir, nil
	default:
		ir, err := protocol.DecodeOpenAIResponse(body)
		if err != nil {
			return protocol.ChatResponse{}, fmt.Errorf("decode openai response: %w", err)
		}
		return ir, nil
	}
}

func encodeChatResponse(ir protocol.ChatResponse, kind providerKind, requestModel string) ([]byte, error) {
	if ir.Model == "" {
		ir.Model = requestModel
	}
	switch kind {
	case kindAnthropic:
		return protocol.EncodeAnthropicResponse(ir)
	case kindGemini:
		clearNativeFinish(&ir)
		return protocol.EncodeGeminiResponse(ir)
	default:
		fillDefaultsForOpenAIResponse(&ir, requestModel)
		return protocol.EncodeOpenAIResponse(ir)
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
	clearNativeFinish(ir)
}

func clearNativeFinish(ir *protocol.ChatResponse) {
	for i := range ir.Choices {
		// NativeFinish preserves vendor-specific stop reasons inside the IR
		// (e.g. Anthropic "end_turn", Gemini "STOP"). When translating to a
		// different wire protocol we must emit that protocol's canonical finish
		// values, so force encoders to use the normalised StopReason field.
		ir.Choices[i].NativeFinish = ""
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
