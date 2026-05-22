package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/protocol"
	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

type streamDelta struct {
	Text       string
	Finish     string
	Usage      protocol.Usage
	HasUsage   bool
	Done       bool
	RawEvent   string
	RawPayload []byte
}

type streamEmitter struct {
	dstKind      providerKind
	requestModel string
	id           string
	created      int64
	started      bool
	textStarted  bool
	finished     bool
	bytesOut     int64
}

func (p *Proxy) serveChatStreamAs(w http.ResponseWriter, resp *http.Response, srcKind, dstKind providerKind, requestedModel, providerName string, rec *store.CallRecord) {
	w.Header().Set("X-AI-Hub-Provider", providerName)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.serveStreamThrough(w, resp, providerName, true, rec)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	rec.Status = http.StatusOK

	flusher, _ := w.(http.Flusher)
	em := &streamEmitter{
		dstKind:      dstKind,
		requestModel: requestedModel,
		id:           "chatcmpl-" + randomID(24),
		created:      time.Now().Unix(),
	}

	err := scanSSE(resp.Body, func(ev sseEvent) error {
		delta, ok, err := parseStreamDelta(srcKind, ev)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		n, err := em.emit(w, delta)
		em.bytesOut += int64(n)
		if flusher != nil && n > 0 {
			flusher.Flush()
		}
		return err
	})
	if err != nil {
		rec.ErrMessage = err.Error()
	}
	if !em.finished {
		n, err := em.emit(w, streamDelta{Done: true})
		em.bytesOut += int64(n)
		if flusher != nil && n > 0 {
			flusher.Flush()
		}
		if err != nil && rec.ErrMessage == "" {
			rec.ErrMessage = err.Error()
		}
	}
	rec.BytesOut = em.bytesOut
}

type sseEvent struct {
	Event string
	Data  []byte
}

func scanSSE(r io.Reader, fn func(sseEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
	var eventName string
	var data bytes.Buffer

	flush := func() error {
		if data.Len() == 0 && eventName == "" {
			return nil
		}
		payload := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
		ev := sseEvent{Event: eventName, Data: append([]byte(nil), payload...)}
		eventName = ""
		data.Reset()
		return fn(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func parseStreamDelta(kind providerKind, ev sseEvent) (streamDelta, bool, error) {
	data := bytes.TrimSpace(ev.Data)
	if len(data) == 0 {
		return streamDelta{}, false, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return streamDelta{Done: true}, true, nil
	}
	switch kind {
	case kindAnthropic:
		return parseAnthropicStreamDelta(ev.Event, data)
	case kindGemini:
		return parseGeminiStreamDelta(data)
	default:
		return parseOpenAIStreamDelta(data)
	}
}

func parseOpenAIStreamDelta(data []byte) (streamDelta, bool, error) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason any `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return streamDelta{}, false, fmt.Errorf("decode openai stream chunk: %w", err)
	}
	var out streamDelta
	if len(chunk.Choices) > 0 {
		out.Text = chunk.Choices[0].Delta.Content
		if s, ok := chunk.Choices[0].FinishReason.(string); ok {
			out.Finish = normaliseOpenAIStreamFinish(s)
		}
	}
	if chunk.Usage != nil {
		out.HasUsage = true
		out.Usage = protocol.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}
	return out, out.Text != "" || out.Finish != "" || out.HasUsage, nil
}

func parseAnthropicStreamDelta(eventName string, data []byte) (streamDelta, bool, error) {
	if eventName == "message_stop" {
		return streamDelta{Done: true}, true, nil
	}
	var chunk struct {
		Type  string `json:"type"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return streamDelta{}, false, fmt.Errorf("decode anthropic stream chunk: %w", err)
	}
	var out streamDelta
	if chunk.Type == "content_block_delta" && chunk.Delta.Text != "" {
		out.Text = chunk.Delta.Text
	}
	if chunk.Type == "message_delta" && chunk.Delta.StopReason != "" {
		out.Finish = normaliseAnthropicStreamFinish(chunk.Delta.StopReason)
	}
	if chunk.Usage != nil {
		out.HasUsage = true
		out.Usage = protocol.Usage{
			PromptTokens:     chunk.Usage.InputTokens,
			CompletionTokens: chunk.Usage.OutputTokens,
			TotalTokens:      chunk.Usage.InputTokens + chunk.Usage.OutputTokens,
		}
	}
	return out, out.Text != "" || out.Finish != "" || out.HasUsage, nil
}

func parseGeminiStreamDelta(data []byte) (streamDelta, bool, error) {
	var chunk struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return streamDelta{}, false, fmt.Errorf("decode gemini stream chunk: %w", err)
	}
	var out streamDelta
	if len(chunk.Candidates) > 0 {
		for _, part := range chunk.Candidates[0].Content.Parts {
			out.Text += part.Text
		}
		if chunk.Candidates[0].FinishReason != "" {
			out.Finish = normaliseGeminiStreamFinish(chunk.Candidates[0].FinishReason)
		}
	}
	if chunk.UsageMetadata != nil {
		out.HasUsage = true
		out.Usage = protocol.Usage{
			PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
			CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
		}
	}
	return out, out.Text != "" || out.Finish != "" || out.HasUsage, nil
}

func (e *streamEmitter) emit(w io.Writer, delta streamDelta) (int, error) {
	switch e.dstKind {
	case kindAnthropic:
		return e.emitAnthropic(w, delta)
	case kindGemini:
		return e.emitGemini(w, delta)
	default:
		return e.emitOpenAI(w, delta)
	}
}

func (e *streamEmitter) emitOpenAI(w io.Writer, delta streamDelta) (int, error) {
	if delta.Done {
		if e.finished {
			return 0, nil
		}
		e.finished = true
		return writeSSE(w, "", []byte("[DONE]"))
	}
	if delta.Text == "" && delta.Finish == "" && !delta.HasUsage {
		return 0, nil
	}
	choice := map[string]any{
		"index": 0,
		"delta": map[string]any{},
	}
	if delta.Text != "" {
		choice["delta"] = map[string]any{"content": delta.Text}
	}
	if delta.Finish != "" {
		choice["finish_reason"] = openAIStreamFinish(delta.Finish)
	} else {
		choice["finish_reason"] = nil
	}
	payload := map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.requestModel,
		"choices": []any{choice},
	}
	if delta.HasUsage {
		payload["usage"] = map[string]any{
			"prompt_tokens":     delta.Usage.PromptTokens,
			"completion_tokens": delta.Usage.CompletionTokens,
			"total_tokens":      delta.Usage.TotalTokens,
		}
	}
	return writeJSONSSE(w, "", payload)
}

func (e *streamEmitter) emitAnthropic(w io.Writer, delta streamDelta) (int, error) {
	var total int
	write := func(event string, payload any) error {
		n, err := writeJSONSSE(w, event, payload)
		total += n
		return err
	}
	if !e.started {
		e.started = true
		if err := write("message_start", map[string]any{
			"type":    "message_start",
			"message": map[string]any{"id": "msg_" + randomID(18), "type": "message", "role": "assistant", "model": e.requestModel, "content": []any{}},
		}); err != nil {
			return total, err
		}
	}
	if delta.Text != "" {
		if !e.textStarted {
			e.textStarted = true
			if err := write("content_block_start", map[string]any{
				"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""},
			}); err != nil {
				return total, err
			}
		}
		if err := write("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": delta.Text},
		}); err != nil {
			return total, err
		}
	}
	if delta.Done || delta.Finish != "" {
		if e.finished {
			return total, nil
		}
		e.finished = true
		if e.textStarted {
			if err := write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}); err != nil {
				return total, err
			}
		}
		finish := anthropicStreamFinish(delta.Finish)
		if finish == "" {
			finish = "end_turn"
		}
		messageDelta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": finish, "stop_sequence": nil}}
		if delta.HasUsage {
			messageDelta["usage"] = map[string]any{"output_tokens": delta.Usage.CompletionTokens}
		}
		if err := write("message_delta", messageDelta); err != nil {
			return total, err
		}
		if err := write("message_stop", map[string]any{"type": "message_stop"}); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (e *streamEmitter) emitGemini(w io.Writer, delta streamDelta) (int, error) {
	if delta.Done {
		e.finished = true
		return 0, nil
	}
	if delta.Text == "" && delta.Finish == "" && !delta.HasUsage {
		return 0, nil
	}
	candidate := map[string]any{"index": 0}
	if delta.Text != "" {
		candidate["content"] = map[string]any{"role": "model", "parts": []any{map[string]any{"text": delta.Text}}}
	}
	if delta.Finish != "" {
		candidate["finishReason"] = geminiStreamFinish(delta.Finish)
		e.finished = true
	}
	payload := map[string]any{"candidates": []any{candidate}}
	if delta.HasUsage {
		payload["usageMetadata"] = map[string]any{
			"promptTokenCount":     delta.Usage.PromptTokens,
			"candidatesTokenCount": delta.Usage.CompletionTokens,
			"totalTokenCount":      delta.Usage.TotalTokens,
		}
	}
	return writeJSONSSE(w, "", payload)
}

func writeJSONSSE(w io.Writer, event string, payload any) (int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	return writeSSE(w, event, b)
}

func writeSSE(w io.Writer, event string, data []byte) (int, error) {
	var b bytes.Buffer
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return w.Write(b.Bytes())
}

func normaliseOpenAIStreamFinish(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "stop":
		return protocol.StopReasonStop
	case "length":
		return protocol.StopReasonLength
	case "tool_calls", "function_call":
		return protocol.StopReasonToolCalls
	case "content_filter":
		return protocol.StopReasonContentFilter
	default:
		return s
	}
}

func normaliseAnthropicStreamFinish(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "end_turn", "stop_sequence":
		return protocol.StopReasonStop
	case "max_tokens":
		return protocol.StopReasonLength
	case "tool_use":
		return protocol.StopReasonToolCalls
	default:
		return s
	}
}

func normaliseGeminiStreamFinish(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "STOP":
		return protocol.StopReasonStop
	case "MAX_TOKENS":
		return protocol.StopReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return protocol.StopReasonContentFilter
	default:
		return s
	}
}

func openAIStreamFinish(s string) string {
	switch s {
	case protocol.StopReasonLength:
		return "length"
	case protocol.StopReasonToolCalls:
		return "tool_calls"
	case protocol.StopReasonContentFilter:
		return "content_filter"
	case protocol.StopReasonStop, "":
		return "stop"
	default:
		return s
	}
}

func anthropicStreamFinish(s string) string {
	switch s {
	case protocol.StopReasonLength:
		return "max_tokens"
	case protocol.StopReasonToolCalls:
		return "tool_use"
	case protocol.StopReasonStop, "":
		return "end_turn"
	default:
		return s
	}
}

func geminiStreamFinish(s string) string {
	switch s {
	case protocol.StopReasonLength:
		return "MAX_TOKENS"
	case protocol.StopReasonContentFilter:
		return "SAFETY"
	case protocol.StopReasonStop, "":
		return "STOP"
	default:
		return strings.ToUpper(s)
	}
}
