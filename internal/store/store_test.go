package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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

func TestCreateInitialAdminNormalizesAndPreventsSecondBootstrap(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	created, err := st.CreateInitialAdmin(ctx, User{Username: " AdminUser ", PasswordHash: "hash", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "adminuser" || created.Role != RoleAdmin || created.ID == 0 {
		t.Fatalf("unexpected initial admin: %+v", created)
	}
	if created.PasswordHash == "" {
		t.Fatalf("expected password hash to be stored")
	}

	_, err = st.CreateInitialAdmin(ctx, User{Username: "other", PasswordHash: "hash", Role: RoleAdmin})
	if !errors.Is(err, ErrInitialAdminExists) {
		t.Fatalf("expected ErrInitialAdminExists, got %v", err)
	}

	count, err := st.CountUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}
}

func TestCreateInitialAdminConcurrentOnlyOneWins(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, username := range []string{"first", "second"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			_, err := st.CreateInitialAdmin(ctx, User{Username: username, PasswordHash: "hash"})
			errs <- err
		}(username)
	}
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, ErrInitialAdminExists) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestCreateUserValidatesAndFindsNormalizedUsername(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	created, err := st.CreateUser(ctx, User{Username: " MixedCase ", PasswordHash: "hash", Role: ""})
	if err != nil {
		t.Fatal(err)
	}
	if created.Username != "mixedcase" || created.Role != RoleUser {
		t.Fatalf("unexpected user: %+v", created)
	}

	found, err := st.FindUserByUsername(ctx, " MIXEDCASE ")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != created.ID || found.PasswordHash != "hash" {
		t.Fatalf("found wrong user: %+v", found)
	}

	if _, err := st.CreateUser(ctx, User{Username: "bad-role", PasswordHash: "hash", Role: "owner"}); err == nil {
		t.Fatalf("expected invalid role error")
	}
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
