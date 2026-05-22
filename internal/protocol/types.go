// Package protocol defines a provider-agnostic intermediate representation
// (IR) for chat completion requests and responses, plus pure-function
// converters between the IR and each supported provider's native format
// (OpenAI, Anthropic, Gemini).
//
// The IR is intentionally narrower than any single provider so the converters
// stay small and obviously correct; provider-specific knobs that don't have
// a natural home in the IR ride along in ChatRequest.Extra and are passed
// through verbatim where it is safe to do so.
//
// Stage 1 covers non-streaming request/response conversion. Streaming event
// translation lives in stream.go and stream_*.go (added in a later stage).
package protocol

// Role values used inside Message.Role.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Part type values used inside Part.Type.
const (
	PartText       = "text"
	PartImage      = "image"
	PartAudio      = "audio"
	PartToolUse    = "tool_use"    // assistant requesting a tool call
	PartToolResult = "tool_result" // tool/user returning a tool call's result
)

// StopReason values normalised across providers.
const (
	StopReasonStop          = "stop"           // natural end / stop sequence
	StopReasonLength        = "length"         // max_tokens reached
	StopReasonToolCalls     = "tool_calls"     // model wants to invoke tools
	StopReasonContentFilter = "content_filter" // upstream safety filter
	StopReasonError         = "error"          // upstream-reported error
)

// ChatRequest is the IR for a chat-style completion request.
//
// Construction rules:
//   - System messages are hoisted into ChatRequest.System; Messages contains
//     only user/assistant/tool turns.
//   - Content is always represented as a Part slice, even when a single text
//     part would suffice. This keeps the converters uniform.
//   - Pointer fields (Temperature, TopP, ToolChoice, ResponseFmt) are nil
//     when unset so the converters can omit them from the provider wire
//     format rather than send a zero value.
type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int // 0 = let the provider decide; Anthropic requires non-zero
	Temperature *float64
	TopP        *float64
	Stop        []string
	Stream      bool
	StreamUsage bool // OpenAI's stream_options.include_usage equivalent
	Tools       []Tool
	ToolChoice  *ToolChoice
	ResponseFmt *ResponseFormat

	// Extra carries provider-specific fields that don't fit the IR (e.g.
	// OpenAI's "seed", Anthropic's "top_k"). Converters MAY forward
	// entries from Extra to their native format when a key matches the
	// target provider's schema; unknown keys are dropped silently.
	Extra map[string]any
}

// Message is one turn in a conversation.
type Message struct {
	Role       string // RoleUser / RoleAssistant / RoleTool
	Name       string // optional speaker name (OpenAI supports this)
	Content    []Part
	ToolCallID string     // when Role == RoleTool, the tool_use id being answered
	ToolCalls  []ToolCall // when Role == RoleAssistant and the model invoked tools
}

// Part represents one slice of a Message.Content. Exactly one of the union
// fields is populated based on Type.
type Part struct {
	Type string // PartText / PartImage / PartAudio / PartToolUse / PartToolResult

	// PartText
	Text string

	// PartImage / PartAudio: at least one of (URL, Data) must be set.
	// Data is raw base64 (without the "data:..." prefix); MediaType is
	// the MIME type, e.g. "image/png".
	URL       string
	Data      string
	MediaType string

	// PartToolUse: a tool call requested by the assistant.
	ToolUse *ToolUse

	// PartToolResult: the result a tool returned for an earlier ToolUse.
	ToolResult *ToolResult
}

// Tool describes a function/tool the model is allowed to call.
type Tool struct {
	Name        string
	Description string
	// Parameters is a JSON Schema describing the tool's arguments. Stored
	// as a generic map so it round-trips through each provider's wire
	// format without lossy re-serialisation.
	Parameters map[string]any
}

// ToolChoice constrains which tools the model may pick. Mode is one of
// "auto" (default), "none", "required", or "specific" (Name must be set).
type ToolChoice struct {
	Mode string
	Name string
}

// ResponseFormat asks the model to constrain its output. Type is one of
// "text" (default), "json_object", or "json_schema". When "json_schema",
// Schema holds the JSON Schema document and SchemaName its identifier.
type ResponseFormat struct {
	Type       string
	Schema     map[string]any
	SchemaName string
	Strict     bool
}

// ToolUse is the assistant asking to call a tool. Arguments are stored
// as a generic value to preserve the upstream's structure.
type ToolUse struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// ToolCall mirrors OpenAI's tool_calls array entry on assistant messages.
// In the IR it's kept alongside ToolUse for round-trip fidelity when the
// caller sent an OpenAI-shaped history.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // OpenAI sends arguments as a JSON-encoded string
}

// ToolResult is the matching response returned by a tool/user message.
type ToolResult struct {
	ToolUseID string
	Content   string // either plain text or JSON-encoded structured result
	IsError   bool
}

// ChatResponse is the IR for a non-streaming completion result.
type ChatResponse struct {
	ID         string
	Model      string
	Created    int64 // unix seconds; converters synthesise this when the provider omits it
	Choices    []Choice
	Usage      Usage
	StopReason string // normalised; see StopReason* constants
}

// Choice is one alternative the model produced. Index is 0-based.
type Choice struct {
	Index        int
	Message      Message
	StopReason   string // per-choice stop reason if the provider supplies one
	LogProbs     map[string]any
	NativeFinish string // raw provider finish_reason for fidelity
}

// Usage is the token accounting block, normalised across providers.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Helper functions used by the converters --------------------------------

// TextPart constructs a Part containing only text. Convenience for the
// converters; not strictly required for callers.
func TextPart(s string) Part { return Part{Type: PartText, Text: s} }

// FloatPtr returns a pointer to v. Used by converters when the source
// format expressed a value that the IR keeps as *float64.
func FloatPtr(v float64) *float64 { return &v }

// MessageText flattens a Message's content into a single string by joining
// every PartText entry with "\n". Non-text parts are skipped. Useful when a
// target provider only supports plain string content.
func MessageText(m Message) string {
	var b []byte
	first := true
	for _, p := range m.Content {
		if p.Type != PartText {
			continue
		}
		if !first {
			b = append(b, '\n')
		}
		first = false
		b = append(b, p.Text...)
	}
	return string(b)
}

// HasNonTextContent reports whether any part of the message is something
// other than text (image, audio, tool_use, tool_result). Converters use
// this to decide between the "string content" and "array content" wire
// shapes that some providers expose.
func HasNonTextContent(m Message) bool {
	for _, p := range m.Content {
		if p.Type != PartText {
			return true
		}
	}
	return false
}
