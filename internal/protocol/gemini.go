package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Gemini (generativelanguage.googleapis.com) wire format for
// :generateContent / :streamGenerateContent.

type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  *geminiGenConfig  `json:"generationConfig,omitempty"`
	SafetySettings    []map[string]any  `json:"safetySettings,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string              `json:"text,omitempty"`
	InlineData       *geminiInlineData   `json:"inlineData,omitempty"`
	FileData         *geminiFileData     `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResp `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFnDecl `json:"functionDeclarations,omitempty"`
}

type geminiFnDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *geminiFnCallCfg `json:"functionCallingConfig,omitempty"`
}

type geminiFnCallCfg struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO | ANY | NONE
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiGenConfig struct {
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"topP,omitempty"`
	TopK             *int           `json:"topK,omitempty"`
	MaxOutputTokens  int            `json:"maxOutputTokens,omitempty"`
	StopSequences    []string       `json:"stopSequences,omitempty"`
	CandidateCount   *int           `json:"candidateCount,omitempty"`
	ResponseMimeType string         `json:"responseMimeType,omitempty"`
	ResponseSchema   map[string]any `json:"responseSchema,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string               `json:"modelVersion,omitempty"`
}

type geminiCandidate struct {
	Content       geminiContent    `json:"content"`
	FinishReason  string           `json:"finishReason,omitempty"`
	Index         int              `json:"index"`
	SafetyRatings []map[string]any `json:"safetyRatings,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// DecodeGeminiChat parses a Gemini :generateContent request body into IR.
func DecodeGeminiChat(body []byte) (ChatRequest, error) {
	var in geminiRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatRequest{}, fmt.Errorf("gemini: parse request: %w", err)
	}
	out := ChatRequest{}
	if in.SystemInstruction != nil {
		out.System = flattenGeminiText(in.SystemInstruction.Parts)
	}
	if in.GenerationConfig != nil {
		gc := in.GenerationConfig
		out.Temperature = gc.Temperature
		out.TopP = gc.TopP
		out.MaxTokens = gc.MaxOutputTokens
		out.Stop = append([]string(nil), gc.StopSequences...)
		if gc.TopK != nil {
			out.Extra = map[string]any{"top_k": *gc.TopK}
		}
		if gc.ResponseMimeType != "" {
			out.ResponseFmt = &ResponseFormat{Type: mapGeminiMime(gc.ResponseMimeType)}
			if gc.ResponseSchema != nil {
				out.ResponseFmt.Schema = gc.ResponseSchema
			}
		}
	}
	if len(in.Tools) > 0 {
		for _, t := range in.Tools {
			for _, fn := range t.FunctionDeclarations {
				out.Tools = append(out.Tools, Tool{
					Name:        fn.Name,
					Description: fn.Description,
					Parameters:  fn.Parameters,
				})
			}
		}
	}
	if in.ToolConfig != nil && in.ToolConfig.FunctionCallingConfig != nil {
		out.ToolChoice = decodeGeminiToolChoice(in.ToolConfig.FunctionCallingConfig)
	}
	for _, c := range in.Contents {
		msg := geminiContentToMessage(c)
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// EncodeGeminiChat serialises an IR ChatRequest into a Gemini
// :generateContent request body.
func EncodeGeminiChat(req ChatRequest) ([]byte, error) {
	out := geminiRequest{}
	if req.System != "" {
		out.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	gc := &geminiGenConfig{
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		StopSequences:   append([]string(nil), req.Stop...),
	}
	if v, ok := req.Extra["top_k"]; ok {
		if n, ok := toInt(v); ok {
			gc.TopK = &n
		}
	}
	if req.ResponseFmt != nil {
		switch req.ResponseFmt.Type {
		case "json_object":
			gc.ResponseMimeType = "application/json"
		case "json_schema":
			gc.ResponseMimeType = "application/json"
			if req.ResponseFmt.Schema != nil {
				gc.ResponseSchema = req.ResponseFmt.Schema
			}
		case "text", "":
			// leave unset
		}
	}
	// Avoid emitting an all-zero GenerationConfig.
	if gc.Temperature != nil || gc.TopP != nil || gc.MaxOutputTokens > 0 ||
		len(gc.StopSequences) > 0 || gc.TopK != nil || gc.ResponseMimeType != "" {
		out.GenerationConfig = gc
	}
	if len(req.Tools) > 0 {
		decls := make([]geminiFnDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := sanitiseGeminiSchema(t.Parameters)
			decls = append(decls, geminiFnDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			})
		}
		out.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}
	if req.ToolChoice != nil {
		out.ToolConfig = &geminiToolConfig{
			FunctionCallingConfig: encodeGeminiToolChoice(req.ToolChoice),
		}
	}
	for _, m := range req.Messages {
		c := messageToGeminiContent(m)
		if len(c.Parts) == 0 {
			continue
		}
		out.Contents = append(out.Contents, c)
	}
	return json.Marshal(out)
}

// DecodeGeminiResponse parses a Gemini :generateContent response into IR.
func DecodeGeminiResponse(body []byte) (ChatResponse, error) {
	var in geminiResponse
	if err := json.Unmarshal(body, &in); err != nil {
		return ChatResponse{}, fmt.Errorf("gemini: parse response: %w", err)
	}
	out := ChatResponse{Model: in.ModelVersion}
	if in.UsageMetadata != nil {
		out.Usage = Usage{
			PromptTokens:     in.UsageMetadata.PromptTokenCount,
			CompletionTokens: in.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      in.UsageMetadata.TotalTokenCount,
		}
	}
	for _, cand := range in.Candidates {
		msg := geminiContentToMessage(cand.Content)
		if msg.Role == "" {
			msg.Role = RoleAssistant
		} else if msg.Role == "model" {
			msg.Role = RoleAssistant
		}
		// Re-derive ToolCalls from any tool_use parts so OpenAI emitters
		// can use them directly.
		for _, p := range msg.Content {
			if p.Type == PartToolUse && p.ToolUse != nil {
				args, _ := json.Marshal(p.ToolUse.Arguments)
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
					ID:        p.ToolUse.ID,
					Name:      p.ToolUse.Name,
					Arguments: string(args),
				})
			}
		}
		out.Choices = append(out.Choices, Choice{
			Index:        cand.Index,
			Message:      msg,
			StopReason:   normaliseGeminiFinish(cand.FinishReason),
			NativeFinish: cand.FinishReason,
		})
	}
	if len(out.Choices) > 0 {
		out.StopReason = out.Choices[0].StopReason
	}
	return out, nil
}

// EncodeGeminiResponse serialises an IR ChatResponse into a Gemini
// :generateContent response body.
func EncodeGeminiResponse(resp ChatResponse) ([]byte, error) {
	out := geminiResponse{
		ModelVersion: resp.Model,
		UsageMetadata: &geminiUsageMetadata{
			PromptTokenCount:     resp.Usage.PromptTokens,
			CandidatesTokenCount: resp.Usage.CompletionTokens,
			TotalTokenCount:      resp.Usage.TotalTokens,
		},
	}
	if out.UsageMetadata.TotalTokenCount == 0 {
		out.UsageMetadata.TotalTokenCount = out.UsageMetadata.PromptTokenCount +
			out.UsageMetadata.CandidatesTokenCount
	}
	for _, c := range resp.Choices {
		cand := geminiCandidate{
			Index:        c.Index,
			FinishReason: geminiFinishFromIR(c.NativeFinish, c.StopReason),
		}
		cand.Content = messageToGeminiContent(c.Message)
		// Gemini emits role="model" for assistant turns; never "assistant".
		if cand.Content.Role == RoleAssistant || cand.Content.Role == "" {
			cand.Content.Role = "model"
		}
		out.Candidates = append(out.Candidates, cand)
	}
	if len(out.Candidates) == 0 {
		out.Candidates = []geminiCandidate{{
			Index:        0,
			FinishReason: "STOP",
			Content:      geminiContent{Role: "model", Parts: []geminiPart{{Text: ""}}},
		}}
	}
	return json.Marshal(out)
}

// --- helpers ------------------------------------------------------------

func geminiContentToMessage(c geminiContent) Message {
	msg := Message{}
	switch strings.ToLower(c.Role) {
	case "user", "":
		msg.Role = RoleUser
	case "model":
		msg.Role = RoleAssistant
	case "function", "tool":
		msg.Role = RoleTool
	default:
		msg.Role = strings.ToLower(c.Role)
	}
	for _, p := range c.Parts {
		switch {
		case p.Text != "":
			msg.Content = append(msg.Content, TextPart(p.Text))
		case p.InlineData != nil:
			kind := PartImage
			if strings.HasPrefix(p.InlineData.MimeType, "audio/") {
				kind = PartAudio
			}
			msg.Content = append(msg.Content, Part{
				Type:      kind,
				Data:      p.InlineData.Data,
				MediaType: p.InlineData.MimeType,
			})
		case p.FileData != nil:
			kind := PartImage
			if strings.HasPrefix(p.FileData.MimeType, "audio/") {
				kind = PartAudio
			}
			msg.Content = append(msg.Content, Part{
				Type:      kind,
				URL:       p.FileData.FileURI,
				MediaType: p.FileData.MimeType,
			})
		case p.FunctionCall != nil:
			msg.Content = append(msg.Content, Part{
				Type: PartToolUse,
				ToolUse: &ToolUse{
					// Gemini does not assign tool_use IDs; synthesise a
					// stable one from the function name so downstream
					// providers can correlate the eventual result.
					ID:        "call_" + p.FunctionCall.Name,
					Name:      p.FunctionCall.Name,
					Arguments: p.FunctionCall.Args,
				},
			})
		case p.FunctionResponse != nil:
			data, _ := json.Marshal(p.FunctionResponse.Response)
			msg.Content = append(msg.Content, Part{
				Type: PartToolResult,
				ToolResult: &ToolResult{
					ToolUseID: "call_" + p.FunctionResponse.Name,
					Content:   string(data),
				},
			})
			// A function response in Gemini is sent from the user side;
			// flip the role accordingly so downstream emitters route it
			// to the tool/user channel correctly.
			msg.Role = RoleTool
		}
	}
	return msg
}

func messageToGeminiContent(m Message) geminiContent {
	c := geminiContent{}
	switch m.Role {
	case RoleUser:
		c.Role = "user"
	case RoleAssistant:
		c.Role = "model"
	case RoleTool:
		c.Role = "function"
	default:
		c.Role = m.Role
	}
	hadToolUseInContent := false
	for _, p := range m.Content {
		if p.Type == PartToolUse {
			hadToolUseInContent = true
			break
		}
	}
	if !hadToolUseInContent {
		for _, tc := range m.ToolCalls {
			c.Parts = append(c.Parts, geminiPart{
				FunctionCall: &geminiFunctionCall{
					Name: tc.Name,
					Args: decodeArguments(tc.Arguments),
				},
			})
		}
	}
	for _, p := range m.Content {
		switch p.Type {
		case PartText:
			if p.Text != "" {
				c.Parts = append(c.Parts, geminiPart{Text: p.Text})
			}
		case PartImage:
			if p.Data != "" {
				c.Parts = append(c.Parts, geminiPart{
					InlineData: &geminiInlineData{
						MimeType: defaultMime(p.MediaType, "image/png"),
						Data:     p.Data,
					},
				})
			} else if p.URL != "" {
				c.Parts = append(c.Parts, geminiPart{
					FileData: &geminiFileData{
						MimeType: p.MediaType,
						FileURI:  p.URL,
					},
				})
			}
		case PartAudio:
			if p.Data != "" {
				c.Parts = append(c.Parts, geminiPart{
					InlineData: &geminiInlineData{
						MimeType: defaultMime(p.MediaType, "audio/wav"),
						Data:     p.Data,
					},
				})
			} else if p.URL != "" {
				c.Parts = append(c.Parts, geminiPart{
					FileData: &geminiFileData{
						MimeType: p.MediaType,
						FileURI:  p.URL,
					},
				})
			}
		case PartToolUse:
			if p.ToolUse == nil {
				continue
			}
			c.Parts = append(c.Parts, geminiPart{
				FunctionCall: &geminiFunctionCall{
					Name: p.ToolUse.Name,
					Args: p.ToolUse.Arguments,
				},
			})
		case PartToolResult:
			if p.ToolResult == nil {
				continue
			}
			resp := decodeToolResultContent(p.ToolResult.Content)
			c.Role = "function"
			c.Parts = append(c.Parts, geminiPart{
				FunctionResponse: &geminiFunctionResp{
					// Gemini correlates results by function name; recover
					// the synthesised "call_<name>" prefix when present.
					Name:     stripCallPrefix(p.ToolResult.ToolUseID),
					Response: resp,
				},
			})
		}
	}
	return c
}

func flattenGeminiText(parts []geminiPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func decodeGeminiToolChoice(c *geminiFnCallCfg) *ToolChoice {
	switch strings.ToUpper(c.Mode) {
	case "AUTO", "":
		return &ToolChoice{Mode: "auto"}
	case "ANY":
		if len(c.AllowedFunctionNames) == 1 {
			return &ToolChoice{Mode: "specific", Name: c.AllowedFunctionNames[0]}
		}
		return &ToolChoice{Mode: "required"}
	case "NONE":
		return &ToolChoice{Mode: "none"}
	}
	return nil
}

func encodeGeminiToolChoice(tc *ToolChoice) *geminiFnCallCfg {
	switch tc.Mode {
	case "required":
		return &geminiFnCallCfg{Mode: "ANY"}
	case "none":
		return &geminiFnCallCfg{Mode: "NONE"}
	case "specific":
		return &geminiFnCallCfg{Mode: "ANY", AllowedFunctionNames: []string{tc.Name}}
	default:
		return &geminiFnCallCfg{Mode: "AUTO"}
	}
}

func normaliseGeminiFinish(s string) string {
	switch strings.ToUpper(s) {
	case "STOP":
		return StopReasonStop
	case "MAX_TOKENS":
		return StopReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return StopReasonContentFilter
	case "":
		return ""
	default:
		return s
	}
}

func geminiFinishFromIR(native, normalised string) string {
	if native != "" {
		return native
	}
	switch normalised {
	case StopReasonLength:
		return "MAX_TOKENS"
	case StopReasonContentFilter:
		return "SAFETY"
	case StopReasonToolCalls:
		return "STOP" // Gemini reports tool use under STOP
	case StopReasonStop, "":
		return "STOP"
	default:
		return strings.ToUpper(normalised)
	}
}

func mapGeminiMime(mt string) string {
	if strings.Contains(strings.ToLower(mt), "json") {
		return "json_object"
	}
	return "text"
}

func defaultMime(mt, fallback string) string {
	if mt == "" {
		return fallback
	}
	return mt
}

func stripCallPrefix(id string) string {
	const p = "call_"
	if strings.HasPrefix(id, p) {
		return id[len(p):]
	}
	return id
}

func decodeToolResultContent(s string) map[string]any {
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err == nil {
		return m
	}
	// Wrap non-JSON strings so the upstream still sees structured data.
	return map[string]any{"content": s}
}

// sanitiseGeminiSchema strips JSON-Schema fields Gemini's function schema
// validator rejects (additionalProperties, $schema, $id, definitions, etc).
// It returns a copy; the input is not mutated.
func sanitiseGeminiSchema(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	var dropKeys = map[string]struct{}{
		"$schema":              {},
		"$id":                  {},
		"definitions":          {},
		"$defs":                {},
		"additionalProperties": {},
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, vv := range x {
				if _, drop := dropKeys[k]; drop {
					continue
				}
				out[k] = walk(vv)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, vv := range x {
				out[i] = walk(vv)
			}
			return out
		default:
			return v
		}
	}
	if m, ok := walk(in).(map[string]any); ok {
		return m
	}
	return in
}
