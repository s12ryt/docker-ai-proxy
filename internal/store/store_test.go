package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLogAndSummarize(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	rows := []CallRecord{
		{Provider: "openai", Model: "gpt-4o-mini", Path: "/v1/chat/completions", Status: 200, LatencyMS: 100, TokensIn: 10, TokensOut: 20},
		{Provider: "openai", Model: "gpt-4o-mini", Path: "/v1/chat/completions", Status: 200, LatencyMS: 200, TokensIn: 5, TokensOut: 15},
		{Provider: "anthropic", Model: "claude", Path: "/v1/chat/completions", Status: 500, LatencyMS: 80},
	}
	for _, r := range rows {
		if err := st.LogCall(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	sum, err := st.Summarize(ctx, 24)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalCalls != 3 {
		t.Fatalf("calls=%d", sum.TotalCalls)
	}
	if sum.TotalErrors != 1 {
		t.Fatalf("errors=%d", sum.TotalErrors)
	}
	if sum.TokensIn != 15 || sum.TokensOut != 35 {
		t.Fatalf("tokens %d/%d", sum.TokensIn, sum.TokensOut)
	}
	ai, ok := sum.Providers["openai"]
	if !ok || ai.Calls != 2 || ai.Errors != 0 {
		t.Fatalf("openai stats wrong: %+v", ai)
	}
	an := sum.Providers["anthropic"]
	if an.Calls != 1 || an.Errors != 1 {
		t.Fatalf("anthropic stats wrong: %+v", an)
	}
}

func TestRecentCalls_Order(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = st.LogCall(ctx, CallRecord{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Provider:  "openai", Model: "m", Path: "/x", Status: 200, LatencyMS: int64(i * 10),
		})
	}
	rows, err := st.RecentCalls(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("len=%d", len(rows))
	}
	// rows are newest-first, so id descending
	if !(rows[0].ID > rows[1].ID && rows[1].ID > rows[2].ID) {
		t.Fatalf("not desc: %+v", rows)
	}
}

func TestSummarize_EmptyWindow(t *testing.T) {
	st := newTestStore(t)
	sum, err := st.Summarize(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalCalls != 0 || sum.AvgLatencyMS != 0 {
		t.Fatalf("expected zero: %+v", sum)
	}
}

func TestApplyRetention_DeletesOldCalls(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	oldCall := CallRecord{Timestamp: now.Add(-48 * time.Hour), Provider: "openai", Model: "old", Path: "/old", Status: 200}
	newCall := CallRecord{Timestamp: now.Add(-2 * time.Hour), Provider: "openai", Model: "new", Path: "/new", Status: 200}
	if err := st.LogCall(ctx, oldCall); err != nil {
		t.Fatal(err)
	}
	if err := st.LogCall(ctx, newCall); err != nil {
		t.Fatal(err)
	}

	deleted, err := st.ApplyRetention(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1", deleted)
	}

	rows, err := st.RecentCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "new" {
		t.Fatalf("retention kept wrong rows: %+v", rows)
	}
}

func TestApplyRetention_Disabled(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.LogCall(ctx, CallRecord{Provider: "openai", Model: "m", Path: "/x", Status: 200}); err != nil {
		t.Fatal(err)
	}
	deleted, err := st.ApplyRetention(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("disabled retention deleted %d rows", deleted)
	}
}
