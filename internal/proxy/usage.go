package proxy

import (
	"encoding/json"

	"github.com/s12ryt/docker-ai-proxy/internal/store"
)

type usageWire struct {
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
		InputTokens      int64 `json:"input_tokens"`
		OutputTokens     int64 `json:"output_tokens"`
	} `json:"usage"`
	UsageMetadata *struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		TotalTokenCount      int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func applyUsageFromJSON(rec *store.CallRecord, body []byte) {
	if rec == nil || len(body) == 0 {
		return
	}
	var wire usageWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return
	}
	if wire.Usage != nil {
		in := firstNonZero(wire.Usage.PromptTokens, wire.Usage.InputTokens)
		out := firstNonZero(wire.Usage.CompletionTokens, wire.Usage.OutputTokens)
		applyTokenUsage(rec, in, out, wire.Usage.TotalTokens)
		return
	}
	if wire.UsageMetadata != nil {
		applyTokenUsage(rec, wire.UsageMetadata.PromptTokenCount, wire.UsageMetadata.CandidatesTokenCount, wire.UsageMetadata.TotalTokenCount)
	}
}

func applyTokenUsage(rec *store.CallRecord, in, out, total int64) {
	if rec == nil {
		return
	}
	if in > 0 {
		rec.TokensIn = in
	}
	if out > 0 {
		rec.TokensOut = out
	}
	if total > 0 {
		switch {
		case rec.TokensIn == 0 && rec.TokensOut > 0:
			rec.TokensIn = maxInt64(total-rec.TokensOut, 0)
		case rec.TokensOut == 0 && rec.TokensIn > 0:
			rec.TokensOut = maxInt64(total-rec.TokensIn, 0)
		case rec.TokensIn == 0 && rec.TokensOut == 0:
			rec.TokensIn = total
		}
	}
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
