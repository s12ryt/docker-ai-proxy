package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeGeminiChat_Basic(t *testing.T) {
	in := []byte(`{
		"systemInstruction":{"parts":[{"text":"be terse"}]},
		"contents":[
			{"role":"user","parts":[{"text":"hi"}]},
			{"role":"model","parts":[{"text":"hello"}]}
		],
		"generationConfig":{"temperature":0.4,"topP":0.9,"topK":40,
			"maxOutputTokens":200,"stopSequences":["</end>"]}
	}`)
	req, err := DecodeGeminiChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.System != "be terse" {
		t.Errorf("system=%q", req.System)
	}
	if req.MaxTokens != 200 || req.Temperature == nil || *req.Temperature != 0.4 {
		t.Errorf("gen config lost: %+v", req)
	}
	if got, _ := toInt(req.Extra["top_k"]); got != 40 {
		t.Errorf("top_k=%v", req.Extra["top_k"])
	}
	if len(req.Messages) != 2 || req.Messages[1].Role != RoleAssistant {
		t.Errorf("messages=%+v", req.Messages)
	}
}

func TestGeminiChat_RoundTrip_WithImage(t *testing.T) {
	src := ChatRequest{
		Model:  "gemini-2.5-flash",
		System: "be brief",
		Messages: []Message{{
			Role: RoleUser,
			Content: []Part{
				TextPart("see this"),
				{Type: PartImage, Data: "BBB", MediaType: "image/jpeg"},
			},
		}},
	}
	body, err := EncodeGeminiChat(src)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"inlineData"`) ||
		!strings.Contains(string(body), `"image/jpeg"`) {
		t.Errorf("encoded missing inlineData: %s", body)
	}
	if !strings.Contains(string(body), `"systemInstruction"`) {
		t.Errorf("encoded missing systemInstruction: %s", body)
	}
	back, err := DecodeGeminiChat(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(back.Messages[0].Content) != 2 ||
		back.Messages[0].Content[1].Data != "BBB" {
		t.Errorf("round-trip lost data: %+v", back.Messages[0].Content)
	}
}

func TestGeminiChat_FunctionCallRoundTrip(t *testing.T) {
	in := []byte(`{
		"contents":[
			{"role":"user","parts":[{"text":"weather?"}]},
			{"role":"model","parts":[
				{"functionCall":{"name":"weather","args":{"city":"nyc"}}}
			]},
			{"role":"function","parts":[
				{"functionResponse":{"name":"weather","response":{"temp":"72"}}}
			]}
		]
	}`)
	req, err := DecodeGeminiChat(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("messages=%d", len(req.Messages))
	}
	asst := req.Messages[1]
	if asst.Role != RoleAssistant || len(asst.Content) == 0 ||
		asst.Content[0].Type != PartToolUse {
		t.Fatalf("assistant tool_use missing: %+v", asst)
	}
	if asst.Content[0].ToolUse.Name != "weather" {
		t.Errorf("tool name=%q", asst.Content[0].ToolUse.Name)
	}
	tr := req.Messages[2]
	if tr.Role != RoleTool {
		t.Errorf("tool result role=%q", tr.Role)
	}
	// Round trip back
	body, err := EncodeGeminiChat(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"functionCall"`) ||
		!strings.Contains(string(body), `"functionResponse"`) {
		t.Errorf("encoded missing function blocks: %s", body)
	}
}

func TestGeminiChat_RoleMapping(t *testing.T) {
	body, err := EncodeGeminiChat(ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: []Part{TextPart("u")}},
			{Role: RoleAssistant, Content: []Part{TextPart("a")}},
		},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out geminiRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Contents[0].Role != "user" || out.Contents[1].Role != "model" {
		t.Errorf("roles=%+v", out.Contents)
	}
}

func TestGeminiChat_ToolChoiceModes(t *testing.T) {
	cases := []struct {
		ir         *ToolChoice
		mode       string
		allowed    bool
		allowedFn  string
	}{
		{&ToolChoice{Mode: "auto"}, "AUTO", false, ""},
		{&ToolChoice{Mode: "required"}, "ANY", false, ""},
		{&ToolChoice{Mode: "none"}, "NONE", false, ""},
		{&ToolChoice{Mode: "specific", Name: "fn1"}, "ANY", true, "fn1"},
	}
	for _, c := range cases {
		body, err := EncodeGeminiChat(ChatRequest{
			ToolChoice: c.ir,
			Messages:   []Message{{Role: RoleUser, Content: []Part{TextPart("a")}}},
		})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var out geminiRequest
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.ToolConfig == nil || out.ToolConfig.FunctionCallingConfig == nil {
			t.Fatalf("toolConfig missing for %+v", c.ir)
		}
		fc := out.ToolConfig.FunctionCallingConfig
		if fc.Mode != c.mode {
			t.Errorf("%+v -> mode %q", c.ir, fc.Mode)
		}
		if c.allowed {
			if len(fc.AllowedFunctionNames) != 1 || fc.AllowedFunctionNames[0] != c.allowedFn {
				t.Errorf("%+v -> allowed=%v", c.ir, fc.AllowedFunctionNames)
			}
		}
	}
}

func TestGeminiResponse_FinishMapping(t *testing.T) {
	cases := []struct {
		raw string
		ir  string
	}{
		{"STOP", StopReasonStop},
		{"MAX_TOKENS", StopReasonLength},
		{"SAFETY", StopReasonContentFilter},
		{"RECITATION", StopReasonContentFilter},
	}
	for _, c := range cases {
		in := []byte(`{"candidates":[{"index":0,"finishReason":"` + c.raw +
			`","content":{"role":"model","parts":[{"text":"x"}]}}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
		resp, err := DecodeGeminiResponse(in)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if resp.StopReason != c.ir {
			t.Errorf("%s -> %q", c.raw, resp.StopReason)
		}
		if resp.Choices[0].NativeFinish != c.raw {
			t.Errorf("native=%q", resp.Choices[0].NativeFinish)
		}
	}
}

func TestGeminiResponse_EncodeFromIR(t *testing.T) {
	resp := ChatResponse{
		Model:      "gemini-test",
		StopReason: StopReasonStop,
		Usage:      Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		Choices: []Choice{{
			Message: Message{Role: RoleAssistant, Content: []Part{TextPart("hi")}},
		}},
	}
	body, err := EncodeGeminiResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"role":"model"`) {
		t.Errorf("expected model role: %s", body)
	}
	if !strings.Contains(string(body), `"finishReason":"STOP"`) {
		t.Errorf("expected STOP: %s", body)
	}
}

func TestGeminiSchemaSanitiser(t *testing.T) {
	in := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"$id":                  "tool",
		"additionalProperties": false,
		"type":                 "object",
		"properties": map[string]any{
			"x": map[string]any{
				"type":                 "string",
				"additionalProperties": false,
			},
		},
		"definitions": map[string]any{"X": map[string]any{"type": "string"}},
	}
	out := sanitiseGeminiSchema(in)
	if _, ok := out["$schema"]; ok {
		t.Errorf("$schema not stripped")
	}
	if _, ok := out["additionalProperties"]; ok {
		t.Errorf("additionalProperties not stripped")
	}
	if _, ok := out["definitions"]; ok {
		t.Errorf("definitions not stripped")
	}
	props := out["properties"].(map[string]any)
	x := props["x"].(map[string]any)
	if _, ok := x["additionalProperties"]; ok {
		t.Errorf("nested additionalProperties not stripped")
	}
}

func TestGeminiChat_ResponseFormatJSON(t *testing.T) {
	body, err := EncodeGeminiChat(ChatRequest{
		ResponseFmt: &ResponseFormat{
			Type: "json_schema",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "string"}},
			},
		},
		Messages: []Message{{Role: RoleUser, Content: []Part{TextPart("hi")}}},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"responseMimeType":"application/json"`) ||
		!strings.Contains(string(body), `"responseSchema"`) {
		t.Errorf("response format missing: %s", body)
	}
}
