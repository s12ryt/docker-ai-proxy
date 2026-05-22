package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Anthropic Messages API wire format.

type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	TopK          *int               `json:"top_k,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	ToolChoice    *anthropicChoice   `json:"tool_choice,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// image / document
	Source *anthropicSource `json:"source,omitempty"`

	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	IsError   bool                    `json:"is_error,omitempty"`
	Content   []anthropicContentBlock `json:"content,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicChoice struct {
	Type string `json:"type"` // "auto" | "any" | "tool" | "none"
	Name string `json:"name,omitempty"`
}

type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Model        string                  `json:"model"`
	Content      []anthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence string                  `json:"stop_sequence,omitempty"`
	Usage        *anthropicUsage         `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// DecodeAnthropicChat parses an Anthropic /v1/messages request body into IR.
func DecodeAnthropicChat(body []byte) (ChatRequest, error) {
	var in anthropicRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatRequest{}, fmt.Errorf("anthropic: parse request: %w", err)
	}
	out := ChatRequest{
		Model:       in.Model,
		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		Stop:        append([]string(nil), in.StopSequences...),
		Stream:      in.Stream,
	}
	if in.TopK != nil {
		out.Extra = map[string]any{"top_k": *in.TopK}
	}
	if len(in.Metadata) > 0 {
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		out.Extra["metadata"] = in.Metadata
	}
	out.System = decodeAnthropicSystem(in.System)

	if len(in.Tools) > 0 {
		out.Tools = make([]Tool, 0, len(in.Tools))
		for _, t := range in.Tools {
			out.Tools = append(out.Tools, Tool{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
	}
	if in.ToolChoice != nil {
		out.ToolChoice = decodeAnthropicToolChoice(in.ToolChoice)
	}

	for _, m := range in.Messages {
		msg := Message{Role: strings.ToLower(m.Role)}
		for _, blk := range m.Content {
			switch strings.ToLower(blk.Type) {
			case "text":
				if blk.Text != "" {
					msg.Content = append(msg.Content, TextPart(blk.Text))
				}
			case "image":
				if blk.Source != nil {
					if blk.Source.Type == "url" {
						msg.Content = append(msg.Content, Part{
							Type: PartImage,
							URL:  blk.Source.URL,
						})
					} else {
						msg.Content = append(msg.Content, Part{
							Type:      PartImage,
							Data:      blk.Source.Data,
							MediaType: blk.Source.MediaType,
						})
					}
				}
			case "tool_use":
				msg.Content = append(msg.Content, Part{
					Type: PartToolUse,
					ToolUse: &ToolUse{
						ID:        blk.ID,
						Name:      blk.Name,
						Arguments: blk.Input,
					},
				})
				args, _ := json.Marshal(blk.Input)
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
					ID:        blk.ID,
					Name:      blk.Name,
					Arguments: string(args),
				})
			case "tool_result":
				msg.Content = append(msg.Content, Part{
					Type: PartToolResult,
					ToolResult: &ToolResult{
						ToolUseID: blk.ToolUseID,
						Content:   flattenAnthropicResult(blk.Content),
						IsError:   blk.IsError,
					},
				})
				// Anthropic represents tool results as a separate user
				// message; the IR keeps the original role but downstream
				// converters look for ToolResult parts irrespective.
			}
		}
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// EncodeAnthropicChat serialises an IR ChatRequest into Anthropic's wire
// format. ChatRequest.MaxTokens is forced to a sane default when zero —
// Anthropic refuses requests without max_tokens.
func EncodeAnthropicChat(req ChatRequest) ([]byte, error) {
	out := anthropicRequest{
		Model:         req.Model,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: append([]string(nil), req.Stop...),
		Stream:        req.Stream,
	}
	if out.MaxTokens == 0 {
		// 4096 matches Anthropic's typical default and is high enough
		// to behave like "unspecified" for most chat use cases without
		// the 422 we'd otherwise get.
		out.MaxTokens = 4096
	}
	if req.System != "" {
		out.System = jsonString(req.System)
	}
	if v, ok := req.Extra["top_k"]; ok {
		if n, ok := toInt(v); ok {
			out.TopK = &n
		}
	}
	if v, ok := req.Extra["metadata"].(map[string]any); ok {
		out.Metadata = v
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out.Tools = append(out.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}
	}
	if req.ToolChoice != nil {
		out.ToolChoice = encodeAnthropicToolChoice(req.ToolChoice)
	}

	for _, m := range req.Messages {
		role := m.Role
		// Anthropic only accepts "user" / "assistant". Tool replies are
		// folded into a "user" message that contains tool_result blocks.
		if role == RoleTool {
			role = RoleUser
		}
		msg := anthropicMessage{Role: role}
		// Materialise IR ToolCalls (e.g. coming from an OpenAI-shaped
		// history) into tool_use blocks if the assistant turn doesn't
		// already carry PartToolUse entries.
		hasToolUse := false
		for _, p := range m.Content {
			if p.Type == PartToolUse {
				hasToolUse = true
				break
			}
		}
		if !hasToolUse {
			for _, tc := range m.ToolCalls {
				msg.Content = append(msg.Content, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: decodeArguments(tc.Arguments),
				})
			}
		}
		for _, p := range m.Content {
			switch p.Type {
			case PartText:
				if p.Text != "" {
					msg.Content = append(msg.Content, anthropicContentBlock{
						Type: "text",
						Text: p.Text,
					})
				}
			case PartImage:
				src := &anthropicSource{}
				if p.URL != "" {
					src.Type = "url"
					src.URL = p.URL
				} else {
					src.Type = "base64"
					src.MediaType = p.MediaType
					if src.MediaType == "" {
						src.MediaType = "image/png"
					}
					src.Data = p.Data
				}
				msg.Content = append(msg.Content, anthropicContentBlock{
					Type:   "image",
					Source: src,
				})
			case PartToolUse:
				if p.ToolUse == nil {
					continue
				}
				msg.Content = append(msg.Content, anthropicContentBlock{
					Type:  "tool_use",
					ID:    p.ToolUse.ID,
					Name:  p.ToolUse.Name,
					Input: p.ToolUse.Arguments,
				})
			case PartToolResult:
				if p.ToolResult == nil {
					continue
				}
				msg.Content = append(msg.Content, anthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: p.ToolResult.ToolUseID,
					IsError:   p.ToolResult.IsError,
					Content: []anthropicContentBlock{{
						Type: "text",
						Text: p.ToolResult.Content,
					}},
				})
			}
		}
		if len(msg.Content) == 0 {
			// Anthropic rejects empty content arrays — emit an empty
			// text block so the turn is preserved.
			msg.Content = []anthropicContentBlock{{Type: "text", Text: ""}}
		}
		out.Messages = append(out.Messages, msg)
	}
	return json.Marshal(out)
}

// DecodeAnthropicResponse parses an Anthropic /v1/messages response into IR.
func DecodeAnthropicResponse(body []byte) (ChatResponse, error) {
	var in anthropicResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatResponse{}, fmt.Errorf("anthropic: parse response: %w", err)
	}
	out := ChatResponse{
		ID:         in.ID,
		Model:      in.Model,
		StopReason: normaliseAnthropicStop(in.StopReason),
	}
	if in.Usage != nil {
		out.Usage = Usage{
			PromptTokens:     in.Usage.InputTokens,
			CompletionTokens: in.Usage.OutputTokens,
			TotalTokens:      in.Usage.InputTokens + in.Usage.OutputTokens,
		}
	}
	msg := Message{Role: RoleAssistant}
	if in.Role != "" {
		msg.Role = strings.ToLower(in.Role)
	}
	for _, blk := range in.Content {
		switch strings.ToLower(blk.Type) {
		case "text":
			if blk.Text != "" {
				msg.Content = append(msg.Content, TextPart(blk.Text))
			}
		case "tool_use":
			args, _ := json.Marshal(blk.Input)
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        blk.ID,
				Name:      blk.Name,
				Arguments: string(args),
			})
			msg.Content = append(msg.Content, Part{
				Type: PartToolUse,
				ToolUse: &ToolUse{
					ID:        blk.ID,
					Name:      blk.Name,
					Arguments: blk.Input,
				},
			})
		}
	}
	out.Choices = []Choice{{
		Index:        0,
		Message:      msg,
		StopReason:   out.StopReason,
		NativeFinish: in.StopReason,
	}}
	return out, nil
}

// EncodeAnthropicResponse serialises an IR ChatResponse into Anthropic's
// /v1/messages response body.
func EncodeAnthropicResponse(resp ChatResponse) ([]byte, error) {
	out := anthropicResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       RoleAssistant,
		Model:      resp.Model,
		StopReason: anthropicStopFromIR(resp.StopReason),
		Usage: &anthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
	if len(resp.Choices) == 0 {
		out.Content = []anthropicContentBlock{{Type: "text", Text: ""}}
		return json.Marshal(out)
	}
	choice := resp.Choices[0]
	emittedToolUse := map[string]bool{}
	for _, p := range choice.Message.Content {
		switch p.Type {
		case PartText:
			if p.Text != "" {
				out.Content = append(out.Content, anthropicContentBlock{
					Type: "text",
					Text: p.Text,
				})
			}
		case PartToolUse:
			if p.ToolUse == nil {
				continue
			}
			emittedToolUse[p.ToolUse.ID] = true
			out.Content = append(out.Content, anthropicContentBlock{
				Type:  "tool_use",
				ID:    p.ToolUse.ID,
				Name:  p.ToolUse.Name,
				Input: p.ToolUse.Arguments,
			})
		}
	}
	for _, tc := range choice.Message.ToolCalls {
		if emittedToolUse[tc.ID] {
			continue
		}
		out.Content = append(out.Content, anthropicContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: decodeArguments(tc.Arguments),
		})
	}
	if len(out.Content) == 0 {
		out.Content = []anthropicContentBlock{{Type: "text", Text: ""}}
	}
	return json.Marshal(out)
}

// --- helpers ------------------------------------------------------------

func decodeAnthropicSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// String form
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array of blocks: concatenate text content.
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if strings.EqualFold(b.Type, "text") && b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func flattenAnthropicResult(blocks []anthropicContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if strings.EqualFold(b.Type, "text") && b.Text != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func decodeAnthropicToolChoice(c *anthropicChoice) *ToolChoice {
	switch strings.ToLower(c.Type) {
	case "auto", "":
		return &ToolChoice{Mode: "auto"}
	case "any":
		return &ToolChoice{Mode: "required"}
	case "none":
		return &ToolChoice{Mode: "none"}
	case "tool":
		return &ToolChoice{Mode: "specific", Name: c.Name}
	}
	return nil
}

func encodeAnthropicToolChoice(tc *ToolChoice) *anthropicChoice {
	switch tc.Mode {
	case "required":
		return &anthropicChoice{Type: "any"}
	case "none":
		return &anthropicChoice{Type: "none"}
	case "specific":
		return &anthropicChoice{Type: "tool", Name: tc.Name}
	default:
		return &anthropicChoice{Type: "auto"}
	}
}

func normaliseAnthropicStop(s string) string {
	switch strings.ToLower(s) {
	case "end_turn", "stop_sequence":
		return StopReasonStop
	case "max_tokens":
		return StopReasonLength
	case "tool_use":
		return StopReasonToolCalls
	case "":
		return ""
	default:
		return s
	}
}

func anthropicStopFromIR(s string) string {
	switch s {
	case StopReasonLength:
		return "max_tokens"
	case StopReasonToolCalls:
		return "tool_use"
	case StopReasonStop, "":
		return "end_turn"
	default:
		return s
	}
}
