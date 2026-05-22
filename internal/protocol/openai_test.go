package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeOpenAIChat_BasicAndSystemHoist(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o-mini",
		"messages":[
			{"role":"system","content":"you are a parrot"},
			{"role":"developer","content":"speak en"},
			{"role":"user","content":"hi"}
		],
		"temperature":0.2,
		"top_p":0.8,
		"max_tokens":256,
		"stop":["\n","</end>"],
		"stream":true,
		"stream_options":{"include_usage":true},
		"seed":42,
		"n":1
	}`)
	req, err := DecodeOpenAIChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "gpt-4o-mini" {
		t.Errorf("model=%q", req.Model)
	}
	if req.System != "you are a parrot\nspeak en" {
		t.Errorf("system=%q", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != RoleUser {
		t.Errorf("messages=%+v", req.Messages)
	}
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Errorf("temperature=%v", req.Temperature)
	}
	if req.MaxTokens != 256 {
		t.Errorf("max_tokens=%d", req.MaxTokens)
	}
	if !req.Stream || !req.StreamUsage {
		t.Errorf("stream flags=%v/%v", req.Stream, req.StreamUsage)
	}
	if len(req.Stop) != 2 {
		t.Errorf("stop=%v", req.Stop)
	}
	if got, _ := toInt(req.Extra["seed"]); got != 42 {
		t.Errorf("seed=%v", req.Extra["seed"])
	}
}

func TestOpenAIChat_RoundTrip_StringContent(t *testing.T) {
	src := ChatRequest{
		Model:       "gpt-4o-mini",
		System:      "be terse",
		Temperature: FloatPtr(0.7),
		MaxTokens:   128,
		Messages: []Message{
			{Role: RoleUser, Content: []Part{TextPart("hello")}},
			{Role: RoleAssistant, Content: []Part{TextPart("hi")}},
		},
	}
	body, err := EncodeOpenAIChat(src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := DecodeOpenAIChat(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.System != "be terse" {
		t.Errorf("system lost: %q", back.System)
	}
	if len(back.Messages) != 2 || back.Messages[1].Content[0].Text != "hi" {
		t.Errorf("messages lost: %+v", back.Messages)
	}
	if back.Temperature == nil || *back.Temperature != 0.7 {
		t.Errorf("temperature lost: %v", back.Temperature)
	}
}

func TestOpenAIChat_ImageContent(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
		]}]
	}`)
	req, err := DecodeOpenAIChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	parts := req.Messages[0].Content
	if len(parts) != 2 || parts[0].Type != PartText || parts[1].Type != PartImage {
		t.Fatalf("parts=%+v", parts)
	}
	if parts[1].Data != "AAA" || parts[1].MediaType != "image/png" {
		t.Errorf("image part=%+v", parts[1])
	}
	body, err := EncodeOpenAIChat(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"image_url"`) {
		t.Errorf("encoded missing image_url: %s", body)
	}
}

func TestOpenAIChat_ToolCallsRoundTrip(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"messages":[
			{"role":"user","content":"weather in nyc?"},
			{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function",
					"function":{"name":"get_weather","arguments":"{\"city\":\"nyc\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"72F"}
		]
	}`)
	req, err := DecodeOpenAIChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages=%d", len(req.Messages))
	}
	if len(req.Messages[1].ToolCalls) != 1 ||
		req.Messages[1].ToolCalls[0].Name != "get_weather" {
		t.Fatalf("toolcall=%+v", req.Messages[1].ToolCalls)
	}
	if req.Messages[2].Role != RoleTool ||
		req.Messages[2].Content[0].Type != PartToolResult ||
		req.Messages[2].Content[0].ToolResult.Content != "72F" ||
		req.Messages[2].Content[0].ToolResult.ToolUseID != "call_1" {
		t.Fatalf("tool result=%+v", req.Messages[2])
	}
	body, err := EncodeOpenAIChat(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// tool message round-trips with tool_call_id and string content.
	if !strings.Contains(string(body), `"tool_call_id":"call_1"`) {
		t.Errorf("encoded missing tool_call_id: %s", body)
	}
}

func TestOpenAIChat_ToolChoice(t *testing.T) {
	cases := map[string]string{
		`"auto"`:     "auto",
		`"none"`:     "none",
		`"required"`: "required",
		`{"type":"function","function":{"name":"my_tool"}}`: "specific",
	}
	for raw, mode := range cases {
		in := []byte(`{"model":"x","messages":[{"role":"user","content":"a"}],"tool_choice":` + raw + `}`)
		req, err := DecodeOpenAIChat(in)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if req.ToolChoice == nil || req.ToolChoice.Mode != mode {
			t.Errorf("%s -> %+v", raw, req.ToolChoice)
		}
	}
}

func TestDecodeOpenAIResponse_FinishMapping(t *testing.T) {
	in := []byte(`{
		"id":"chatcmpl-1","model":"gpt-4o","created":1700000000,
		"choices":[{"index":0,"finish_reason":"length",
			"message":{"role":"assistant","content":"truncated"}}],
		"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}
	}`)
	resp, err := DecodeOpenAIResponse(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StopReason != StopReasonLength {
		t.Errorf("stop=%q", resp.StopReason)
	}
	if resp.Choices[0].NativeFinish != "length" {
		t.Errorf("native=%q", resp.Choices[0].NativeFinish)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("usage=%+v", resp.Usage)
	}
}

func TestEncodeOpenAIResponse_EmptyChoicesGetsPadding(t *testing.T) {
	body, err := EncodeOpenAIResponse(ChatResponse{ID: "x", Model: "m"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := out["choices"].([]any); len(got) != 1 {
		t.Errorf("choices=%v", out["choices"])
	}
}

func TestOpenAIResponse_ToolUsePartRendersAsToolCalls(t *testing.T) {
	// IR has only PartToolUse (e.g. translated from Anthropic).
	src := ChatResponse{
		ID: "r1", Model: "claude-mapped", StopReason: StopReasonToolCalls,
		Choices: []Choice{{
			Index:      0,
			StopReason: StopReasonToolCalls,
			Message: Message{
				Role: RoleAssistant,
				Content: []Part{{
					Type: PartToolUse,
					ToolUse: &ToolUse{
						ID: "toolu_1", Name: "lookup",
						Arguments: map[string]any{"q": "x"},
					},
				}},
			},
		}},
	}
	body, err := EncodeOpenAIResponse(src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out openAIChatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Choices) != 1 || len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls missing: %+v", out)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish=%q", out.Choices[0].FinishReason)
	}
	tc := out.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "lookup" || !strings.Contains(tc.Function.Arguments, `"q":"x"`) {
		t.Errorf("toolcall=%+v", tc)
	}
}

func TestOpenAIChat_MessageTextHelper(t *testing.T) {
	m := Message{Content: []Part{TextPart("a"), TextPart("b"), {Type: PartImage}}}
	if MessageText(m) != "a\nb" {
		t.Errorf("MessageText=%q", MessageText(m))
	}
	if !HasNonTextContent(m) {
		t.Errorf("HasNonTextContent should be true")
	}
}

func TestSplitDataURL(t *testing.T) {
	url, data, mt := splitDataURL("data:image/png;base64,XYZ")
	if url != "" || data != "XYZ" || mt != "image/png" {
		t.Errorf("data url parse: %q %q %q", url, data, mt)
	}
	url, data, mt = splitDataURL("https://example.com/x.png")
	if url != "https://example.com/x.png" || data != "" || mt != "" {
		t.Errorf("https parse: %q %q %q", url, data, mt)
	}
}
