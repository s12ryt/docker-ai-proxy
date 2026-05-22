package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAI wire-format structs.
//
// Kept private to this file; callers always work through DecodeOpenAIChat /
// EncodeOpenAIChat / EncodeOpenAIResponse / DecodeOpenAIResponse so the IR
// is the only thing the rest of the codebase sees.

type openAIChatRequest struct {
	Model            string                `json:"model"`
	Messages         []openAIInputMessage  `json:"messages"`
	MaxTokens        int                   `json:"max_tokens,omitempty"`
	MaxOutputTokens  int                   `json:"max_completion_tokens,omitempty"`
	Temperature      *float64              `json:"temperature,omitempty"`
	TopP             *float64              `json:"top_p,omitempty"`
	Stop             json.RawMessage       `json:"stop,omitempty"`
	Stream           bool                  `json:"stream,omitempty"`
	StreamOptions    *openAIStreamOptions  `json:"stream_options,omitempty"`
	Tools            []openAITool          `json:"tools,omitempty"`
	ToolChoice       json.RawMessage       `json:"tool_choice,omitempty"`
	ResponseFormat   *openAIResponseFormat `json:"response_format,omitempty"`
	N                *int                  `json:"n,omitempty"`
	Seed             *int                  `json:"seed,omitempty"`
	User             string                `json:"user,omitempty"`
	FrequencyPenalty *float64              `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64              `json:"presence_penalty,omitempty"`
	LogProbs         *bool                 `json:"logprobs,omitempty"`
	TopLogProbs      *int                  `json:"top_logprobs,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type openAIInputMessage struct {
	Role       string          `json:"role"`
	Name       string          `json:"name,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []openAIToolCal `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type openAIToolCal struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponseFormat struct {
	Type       string                  `json:"type"`
	JSONSchema *openAIJSONSchemaFormat `json:"json_schema,omitempty"`
}

type openAIJSONSchemaFormat struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema,omitempty"`
	Strict bool           `json:"strict,omitempty"`
}

type openAIContentPart struct {
	Type       string            `json:"type"`
	Text       string            `json:"text,omitempty"`
	ImageURL   *openAIImageURL   `json:"image_url,omitempty"`
	InputAudio *openAIInputAudio `json:"input_audio,omitempty"`
}

type openAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type openAIInputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

type openAIChatResponse struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []openAIChoice `json:"choices"`
	Usage             *openAIUsage   `json:"usage,omitempty"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
}

type openAIChoice struct {
	Index        int            `json:"index"`
	Message      openAIRespMsg  `json:"message"`
	FinishReason string         `json:"finish_reason,omitempty"`
	LogProbs     map[string]any `json:"logprobs,omitempty"`
}

type openAIRespMsg struct {
	Role      string          `json:"role"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []openAIToolCal `json:"tool_calls,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DecodeOpenAIChat parses an OpenAI /v1/chat/completions request body into IR.
//
// System messages are hoisted out of Messages into ChatRequest.System (the
// first system message wins; subsequent ones are concatenated with "\n").
// Content is normalised to []Part regardless of whether the input used the
// string or array form.
func DecodeOpenAIChat(body []byte) (ChatRequest, error) {
	var in openAIChatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatRequest{}, fmt.Errorf("openai: parse request: %w", err)
	}

	out := ChatRequest{
		Model:       in.Model,
		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		Stream:      in.Stream,
	}
	if in.MaxOutputTokens > 0 && out.MaxTokens == 0 {
		// newer field; treat as alias when classic max_tokens is unset.
		out.MaxTokens = in.MaxOutputTokens
	}
	if in.StreamOptions != nil {
		out.StreamUsage = in.StreamOptions.IncludeUsage
	}
	if len(in.Stop) > 0 {
		out.Stop = decodeStringList(in.Stop)
	}
	if len(in.Tools) > 0 {
		out.Tools = make([]Tool, 0, len(in.Tools))
		for _, t := range in.Tools {
			if !strings.EqualFold(t.Type, "function") || t.Function.Name == "" {
				continue
			}
			out.Tools = append(out.Tools, Tool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
	}
	if len(in.ToolChoice) > 0 {
		out.ToolChoice = decodeOpenAIToolChoice(in.ToolChoice)
	}
	if in.ResponseFormat != nil {
		out.ResponseFmt = &ResponseFormat{Type: in.ResponseFormat.Type}
		if in.ResponseFormat.JSONSchema != nil {
			out.ResponseFmt.Schema = in.ResponseFormat.JSONSchema.Schema
			out.ResponseFmt.SchemaName = in.ResponseFormat.JSONSchema.Name
			out.ResponseFmt.Strict = in.ResponseFormat.JSONSchema.Strict
		}
	}

	// Carry forward niche knobs through Extra; targets that understand
	// them (mostly OpenAI itself) can pluck them back out on encode.
	extra := map[string]any{}
	if in.N != nil {
		extra["n"] = *in.N
	}
	if in.Seed != nil {
		extra["seed"] = *in.Seed
	}
	if in.User != "" {
		extra["user"] = in.User
	}
	if in.FrequencyPenalty != nil {
		extra["frequency_penalty"] = *in.FrequencyPenalty
	}
	if in.PresencePenalty != nil {
		extra["presence_penalty"] = *in.PresencePenalty
	}
	if in.LogProbs != nil {
		extra["logprobs"] = *in.LogProbs
	}
	if in.TopLogProbs != nil {
		extra["top_logprobs"] = *in.TopLogProbs
	}
	if len(extra) > 0 {
		out.Extra = extra
	}

	var systemParts []string
	for _, m := range in.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == RoleSystem || role == "developer" {
			// developer role (OpenAI o1+) is semantically system-level.
			if s := decodeOpenAIContentToText(m.Content); s != "" {
				systemParts = append(systemParts, s)
			}
			continue
		}
		msg := Message{Role: role, Name: m.Name, ToolCallID: m.ToolCallID}
		msg.Content = decodeOpenAIContentToParts(m.Content)
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		// In OpenAI, a tool-result message carries its result in Content
		// (string) plus tool_call_id. Lift it into a ToolResult part so
		// downstream converters can render it natively.
		if role == RoleTool {
			text := MessageText(msg)
			msg.Content = []Part{{
				Type: PartToolResult,
				ToolResult: &ToolResult{
					ToolUseID: m.ToolCallID,
					Content:   text,
				},
			}}
		}
		out.Messages = append(out.Messages, msg)
	}
	if len(systemParts) > 0 {
		out.System = strings.Join(systemParts, "\n")
	}
	return out, nil
}

// EncodeOpenAIChat serialises an IR ChatRequest into an OpenAI chat
// completions request body. The model field is taken verbatim; callers
// that need to remap the model name should do so before calling.
func EncodeOpenAIChat(req ChatRequest) ([]byte, error) {
	out := openAIChatRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
	}
	if req.StreamUsage {
		out.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	if len(req.Stop) > 0 {
		out.Stop = encodeStringList(req.Stop)
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]openAITool, 0, len(req.Tools))
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, openAITool{
				Type: "function",
				Function: openAIToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
	}
	if req.ToolChoice != nil {
		out.ToolChoice = encodeOpenAIToolChoice(req.ToolChoice)
	}
	if req.ResponseFmt != nil {
		rf := &openAIResponseFormat{Type: req.ResponseFmt.Type}
		if req.ResponseFmt.Schema != nil {
			rf.JSONSchema = &openAIJSONSchemaFormat{
				Name:   req.ResponseFmt.SchemaName,
				Schema: req.ResponseFmt.Schema,
				Strict: req.ResponseFmt.Strict,
			}
		}
		out.ResponseFormat = rf
	}

	// Re-attach the OpenAI-native knobs we preserved on decode.
	if v, ok := req.Extra["n"]; ok {
		if n, ok := toInt(v); ok {
			out.N = &n
		}
	}
	if v, ok := req.Extra["seed"]; ok {
		if n, ok := toInt(v); ok {
			out.Seed = &n
		}
	}
	if v, ok := req.Extra["user"].(string); ok && v != "" {
		out.User = v
	}
	if v, ok := req.Extra["frequency_penalty"]; ok {
		if f, ok := toFloat(v); ok {
			out.FrequencyPenalty = &f
		}
	}
	if v, ok := req.Extra["presence_penalty"]; ok {
		if f, ok := toFloat(v); ok {
			out.PresencePenalty = &f
		}
	}
	if v, ok := req.Extra["logprobs"].(bool); ok {
		out.LogProbs = &v
	}
	if v, ok := req.Extra["top_logprobs"]; ok {
		if n, ok := toInt(v); ok {
			out.TopLogProbs = &n
		}
	}

	if req.System != "" {
		out.Messages = append(out.Messages, openAIInputMessage{
			Role:    RoleSystem,
			Content: jsonString(req.System),
		})
	}
	for _, m := range req.Messages {
		om := openAIInputMessage{Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID}
		if m.Role == RoleTool {
			// Surface the tool result as plain string content so the
			// upstream sees the canonical OpenAI shape.
			var text string
			for _, p := range m.Content {
				if p.Type == PartToolResult && p.ToolResult != nil {
					text = p.ToolResult.Content
					if om.ToolCallID == "" {
						om.ToolCallID = p.ToolResult.ToolUseID
					}
					break
				}
			}
			om.Content = jsonString(text)
		} else {
			om.Content = encodeOpenAIContent(m.Content)
		}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, openAIToolCal{
				ID:   tc.ID,
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out.Messages = append(out.Messages, om)
	}
	return json.Marshal(out)
}

// DecodeOpenAIResponse parses an OpenAI chat completion response into IR.
func DecodeOpenAIResponse(body []byte) (ChatResponse, error) {
	var in openAIChatResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatResponse{}, fmt.Errorf("openai: parse response: %w", err)
	}
	out := ChatResponse{
		ID:      in.ID,
		Model:   in.Model,
		Created: in.Created,
	}
	if in.Usage != nil {
		out.Usage = Usage{
			PromptTokens:     in.Usage.PromptTokens,
			CompletionTokens: in.Usage.CompletionTokens,
			TotalTokens:      in.Usage.TotalTokens,
		}
	}
	for _, c := range in.Choices {
		msg := Message{Role: c.Message.Role}
		if c.Message.Content != "" {
			msg.Content = []Part{TextPart(c.Message.Content)}
		}
		for _, tc := range c.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
			msg.Content = append(msg.Content, Part{
				Type: PartToolUse,
				ToolUse: &ToolUse{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: decodeArguments(tc.Function.Arguments),
				},
			})
		}
		out.Choices = append(out.Choices, Choice{
			Index:        c.Index,
			Message:      msg,
			StopReason:   normaliseOpenAIFinish(c.FinishReason),
			NativeFinish: c.FinishReason,
			LogProbs:     c.LogProbs,
		})
	}
	if len(out.Choices) > 0 {
		out.StopReason = out.Choices[0].StopReason
	}
	return out, nil
}

// EncodeOpenAIResponse serialises an IR ChatResponse into an OpenAI chat
// completion response body.
func EncodeOpenAIResponse(resp ChatResponse) ([]byte, error) {
	out := openAIChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.Created,
		Model:   resp.Model,
		Usage: &openAIUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	if out.Usage.TotalTokens == 0 {
		out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
	}
	for _, c := range resp.Choices {
		ch := openAIChoice{
			Index:        c.Index,
			FinishReason: openAIFinishFromIR(c.NativeFinish, c.StopReason),
			LogProbs:     c.LogProbs,
		}
		ch.Message.Role = c.Message.Role
		if ch.Message.Role == "" {
			ch.Message.Role = RoleAssistant
		}
		ch.Message.Content = MessageText(c.Message)
		for _, tc := range c.Message.ToolCalls {
			ch.Message.ToolCalls = append(ch.Message.ToolCalls, openAIToolCal{
				ID:   tc.ID,
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		// If we only have IR-side tool_use parts (translated from
		// Anthropic/Gemini) and no OpenAI-shaped ToolCalls, materialise
		// them on the way out.
		if len(ch.Message.ToolCalls) == 0 {
			for _, p := range c.Message.Content {
				if p.Type != PartToolUse || p.ToolUse == nil {
					continue
				}
				args, _ := json.Marshal(p.ToolUse.Arguments)
				ch.Message.ToolCalls = append(ch.Message.ToolCalls, openAIToolCal{
					ID:   p.ToolUse.ID,
					Type: "function",
					Function: openAIToolCallFunction{
						Name:      p.ToolUse.Name,
						Arguments: string(args),
					},
				})
			}
		}
		out.Choices = append(out.Choices, ch)
	}
	if len(out.Choices) == 0 {
		// Always emit at least one choice — downstream OpenAI SDKs
		// trip on an empty array.
		out.Choices = []openAIChoice{{
			Index:        0,
			Message:      openAIRespMsg{Role: RoleAssistant},
			FinishReason: openAIFinishFromIR("", resp.StopReason),
		}}
	}
	return json.Marshal(out)
}

// --- helpers ------------------------------------------------------------

func decodeOpenAIContentToParts(raw json.RawMessage) []Part {
	if len(raw) == 0 {
		return nil
	}
	// String form
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []Part{TextPart(s)}
	}
	// Array form
	var arr []openAIContentPart
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	parts := make([]Part, 0, len(arr))
	for _, p := range arr {
		switch strings.ToLower(p.Type) {
		case "text", "input_text", "output_text":
			if p.Text != "" {
				parts = append(parts, TextPart(p.Text))
			}
		case "image_url", "input_image":
			if p.ImageURL != nil {
				url, data, mt := splitDataURL(p.ImageURL.URL)
				parts = append(parts, Part{
					Type:      PartImage,
					URL:       url,
					Data:      data,
					MediaType: mt,
				})
			}
		case "input_audio":
			if p.InputAudio != nil {
				parts = append(parts, Part{
					Type:      PartAudio,
					Data:      p.InputAudio.Data,
					MediaType: "audio/" + p.InputAudio.Format,
				})
			}
		}
	}
	return parts
}

func decodeOpenAIContentToText(raw json.RawMessage) string {
	parts := decodeOpenAIContentToParts(raw)
	if len(parts) == 0 {
		return ""
	}
	return MessageText(Message{Content: parts})
}

func encodeOpenAIContent(parts []Part) json.RawMessage {
	// Fast path: pure-text content → emit as string for maximum
	// compatibility with older OpenAI-compatible servers.
	allText := true
	for _, p := range parts {
		if p.Type != PartText {
			allText = false
			break
		}
	}
	if allText {
		return jsonString(MessageText(Message{Content: parts}))
	}
	arr := make([]openAIContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case PartText:
			arr = append(arr, openAIContentPart{Type: "text", Text: p.Text})
		case PartImage:
			url := p.URL
			if url == "" && p.Data != "" {
				mt := p.MediaType
				if mt == "" {
					mt = "image/png"
				}
				url = "data:" + mt + ";base64," + p.Data
			}
			arr = append(arr, openAIContentPart{
				Type:     "image_url",
				ImageURL: &openAIImageURL{URL: url},
			})
		case PartAudio:
			format := strings.TrimPrefix(p.MediaType, "audio/")
			if format == "" {
				format = "wav"
			}
			arr = append(arr, openAIContentPart{
				Type:       "input_audio",
				InputAudio: &openAIInputAudio{Data: p.Data, Format: format},
			})
		case PartToolUse, PartToolResult:
			// Tool turns are encoded via the dedicated paths in
			// EncodeOpenAIChat; nothing to emit here.
		}
	}
	if len(arr) == 0 {
		return jsonString("")
	}
	b, _ := json.Marshal(arr)
	return b
}

func decodeOpenAIToolChoice(raw json.RawMessage) *ToolChoice {
	// "auto" | "none" | "required" | {"type":"function","function":{"name":...}}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(s) {
		case "auto", "":
			return &ToolChoice{Mode: "auto"}
		case "none":
			return &ToolChoice{Mode: "none"}
		case "required":
			return &ToolChoice{Mode: "required"}
		}
		return &ToolChoice{Mode: "auto"}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Function.Name != "" {
		return &ToolChoice{Mode: "specific", Name: obj.Function.Name}
	}
	return nil
}

func encodeOpenAIToolChoice(tc *ToolChoice) json.RawMessage {
	switch tc.Mode {
	case "specific":
		b, _ := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		})
		return b
	case "none":
		return jsonString("none")
	case "required":
		return jsonString("required")
	default:
		return jsonString("auto")
	}
}

func normaliseOpenAIFinish(s string) string {
	switch strings.ToLower(s) {
	case "stop":
		return StopReasonStop
	case "length":
		return StopReasonLength
	case "tool_calls", "function_call":
		return StopReasonToolCalls
	case "content_filter":
		return StopReasonContentFilter
	case "":
		return ""
	default:
		return s
	}
}

func openAIFinishFromIR(native, normalised string) string {
	if native != "" {
		return native
	}
	switch normalised {
	case StopReasonLength:
		return "length"
	case StopReasonToolCalls:
		return "tool_calls"
	case StopReasonContentFilter:
		return "content_filter"
	case StopReasonStop, "":
		return "stop"
	default:
		return normalised
	}
}

func decodeStringList(raw json.RawMessage) []string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []string{s}
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}

func encodeStringList(in []string) json.RawMessage {
	if len(in) == 1 {
		return jsonString(in[0])
	}
	b, _ := json.Marshal(in)
	return b
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// splitDataURL recognises "data:<mime>;base64,<payload>" and returns
// ("", payload, mime). Plain URLs are returned as (url, "", "").
func splitDataURL(s string) (url, data, mime string) {
	if !strings.HasPrefix(s, "data:") {
		return s, "", ""
	}
	rest := strings.TrimPrefix(s, "data:")
	semi := strings.IndexByte(rest, ';')
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return s, "", ""
	}
	if semi >= 0 && semi < comma {
		mime = rest[:semi]
	} else {
		mime = rest[:comma]
	}
	data = rest[comma+1:]
	return "", data, mime
}

func decodeArguments(s string) map[string]any {
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), true
		}
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}
