package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeAnthropicChat_Basic(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4",
		"max_tokens":1024,
		"system":"be brief",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"}]}
		],
		"temperature":0.3,
		"top_p":0.9,
		"top_k":40,
		"stop_sequences":["</end>"]
	}`)
	req, err := DecodeAnthropicChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "claude-sonnet-4" || req.MaxTokens != 1024 || req.System != "be brief" {
		t.Errorf("basic fields wrong: %+v", req)
	}
	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Errorf("temperature=%v", req.Temperature)
	}
	if got, _ := toInt(req.Extra["top_k"]); got != 40 {
		t.Errorf("top_k=%v", req.Extra["top_k"])
	}
	if len(req.Stop) != 1 || req.Stop[0] != "</end>" {
		t.Errorf("stop=%v", req.Stop)
	}
}

func TestDecodeAnthropicChat_SystemArray(t *testing.T) {
	in := []byte(`{
		"model":"claude","max_tokens":10,
		"system":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)
	req, err := DecodeAnthropicChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.System != "line1\nline2" {
		t.Errorf("system=%q", req.System)
	}
}

func TestAnthropicChat_RoundTrip_WithImage(t *testing.T) {
	src := ChatRequest{
		Model:     "claude",
		MaxTokens: 512,
		System:    "be vivid",
		Messages: []Message{{
			Role: RoleUser,
			Content: []Part{
				TextPart("describe"),
				{Type: PartImage, Data: "AAA", MediaType: "image/jpeg"},
			},
		}},
	}
	body, err := EncodeAnthropicChat(src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"media_type":"image/jpeg"`) {
		t.Errorf("encoded missing media_type: %s", body)
	}
	back, err := DecodeAnthropicChat(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.System != "be vivid" || len(back.Messages[0].Content) != 2 ||
		back.Messages[0].Content[1].MediaType != "image/jpeg" {
		t.Errorf("round-trip lost data: %+v", back)
	}
}

func TestAnthropicChat_DefaultMaxTokens(t *testing.T) {
	body, err := EncodeAnthropicChat(ChatRequest{
		Model:    "claude",
		Messages: []Message{{Role: RoleUser, Content: []Part{TextPart("hi")}}},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out anthropicRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.MaxTokens != 4096 {
		t.Errorf("expected default max_tokens=4096, got %d", out.MaxTokens)
	}
}

func TestAnthropicChat_ToolUseAndResult(t *testing.T) {
	in := []byte(`{
		"model":"claude","max_tokens":1024,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"weather?"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"sf"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1",
					"content":[{"type":"text","text":"70F"}]}
			]}
		]
	}`)
	req, err := DecodeAnthropicChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages=%d", len(req.Messages))
	}
	// assistant carries tool_use
	asst := req.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("toolcalls=%+v", asst.ToolCalls)
	}
	// user-side tool_result
	last := req.Messages[2]
	var found *ToolResult
	for _, p := range last.Content {
		if p.Type == PartToolResult {
			found = p.ToolResult
		}
	}
	if found == nil || found.ToolUseID != "toolu_1" || found.Content != "70F" {
		t.Errorf("tool_result=%+v", found)
	}
	// Now round-trip back to Anthropic body and make sure both blocks survive.
	body, err := EncodeAnthropicChat(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"tool_use"`) ||
		!strings.Contains(string(body), `"tool_result"`) {
		t.Errorf("encoded missing blocks: %s", body)
	}
}

func TestAnthropicChat_ToolChoiceMapping(t *testing.T) {
	cases := []struct {
		raw  string
		mode string
	}{
		{`{"type":"auto"}`, "auto"},
		{`{"type":"any"}`, "required"},
		{`{"type":"none"}`, "none"},
		{`{"type":"tool","name":"my_tool"}`, "specific"},
	}
	for _, c := range cases {
		in := []byte(`{"model":"c","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"a"}]}],"tool_choice":` + c.raw + `}`)
		req, err := DecodeAnthropicChat(in)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if req.ToolChoice == nil || req.ToolChoice.Mode != c.mode {
			t.Errorf("%s -> %+v", c.raw, req.ToolChoice)
		}
		body, err := EncodeAnthropicChat(req)
		if err != nil {
			t.Fatalf("%s encode: %v", c.raw, err)
		}
		var out anthropicRequest
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s unmarshal: %v", c.raw, err)
		}
		if out.ToolChoice == nil {
			t.Errorf("%s tool_choice not emitted", c.raw)
		}
	}
}

func TestAnthropicResponse_StopReasonMapping(t *testing.T) {
	cases := []struct {
		raw string
		ir  string
	}{
		{"end_turn", StopReasonStop},
		{"max_tokens", StopReasonLength},
		{"tool_use", StopReasonToolCalls},
		{"stop_sequence", StopReasonStop},
	}
	for _, c := range cases {
		in := []byte(`{"id":"x","type":"message","role":"assistant","model":"c",
			"content":[{"type":"text","text":"a"}],"stop_reason":"` + c.raw + `",
			"usage":{"input_tokens":1,"output_tokens":2}}`)
		resp, err := DecodeAnthropicResponse(in)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if resp.StopReason != c.ir {
			t.Errorf("%s -> %q", c.raw, resp.StopReason)
		}
	}
}

func TestAnthropicResponse_EncodeWithToolUse(t *testing.T) {
	resp := ChatResponse{
		ID: "r", Model: "c", StopReason: StopReasonToolCalls,
		Usage: Usage{PromptTokens: 3, CompletionTokens: 4},
		Choices: []Choice{{
			Message: Message{Role: RoleAssistant, Content: []Part{
				TextPart("calling tool"),
				{Type: PartToolUse, ToolUse: &ToolUse{
					ID: "toolu_1", Name: "calc",
					Arguments: map[string]any{"x": 1},
				}},
			}},
		}},
	}
	body, err := EncodeAnthropicResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"stop_reason":"tool_use"`) ||
		!strings.Contains(string(body), `"toolu_1"`) {
		t.Errorf("encoded missing fields: %s", body)
	}
}

func TestAnthropicChat_ToolMessageBecomesUser(t *testing.T) {
	src := ChatRequest{
		Model: "c", MaxTokens: 100,
		Messages: []Message{{
			Role:       RoleTool,
			ToolCallID: "toolu_1",
			Content: []Part{{
				Type:       PartToolResult,
				ToolResult: &ToolResult{ToolUseID: "toolu_1", Content: "42"},
			}},
		}},
	}
	body, err := EncodeAnthropicChat(src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out anthropicRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Errorf("expected single user msg, got %+v", out.Messages)
	}
	if len(out.Messages[0].Content) != 1 || out.Messages[0].Content[0].Type != "tool_result" {
		t.Errorf("expected tool_result block: %+v", out.Messages[0].Content)
	}
}
