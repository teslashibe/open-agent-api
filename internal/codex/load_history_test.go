package codex

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoadHistoryPersistsAndAggregatesByAccountAndModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry", "usage.json")
	now := time.Date(2026, 9, 23, 22, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	history, err := OpenLoadHistory(path, []string{"primary", "secondary"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Record(LoadEvent{Account: "primary", Model: "gpt-6-sol", Requests: 1, InputTokens: 120, OutputTokens: 45, InputTokenSource: "upstream"}); err != nil {
		t.Fatal(err)
	}
	if err := history.Record(LoadEvent{Account: "primary", Model: "gpt-6-sol", Requests: 1, InputTokens: 80, OutputTokens: 20, InputTokenSource: "upstream"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	reloaded, err := OpenLoadHistory(path, []string{"primary", "secondary"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reloaded.Snapshot(map[string]int{"secondary": 2})
	if snapshot.Window != "7d" || len(snapshot.Accounts) != 2 || len(snapshot.Models) != 1 {
		t.Fatalf("snapshot shape = %#v", snapshot)
	}
	primary := snapshot.Accounts[0]
	if primary.Label != "primary" || primary.Requests7d != 2 || primary.Requests1h != 2 || primary.Requests24h != 2 || primary.InputTokens7d != 200 || primary.OutputTokens7d != 65 {
		t.Fatalf("primary aggregate = %#v", primary)
	}
	if snapshot.Accounts[1].InFlight != 2 {
		t.Fatalf("secondary inflight = %d", snapshot.Accounts[1].InFlight)
	}
	model := snapshot.Models[0]
	if model.Model != "gpt-6-sol" || model.Account != "primary" || model.Requests != 2 || model.InputTokens != 200 || model.OutputTokens != 65 {
		t.Fatalf("model aggregate = %#v", model)
	}
}

func TestLoadHistoryConcurrentRecordsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 9, 23, 22, 0, 0, 0, time.UTC)
	history, err := OpenLoadHistory(path, []string{"primary"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	const workers = 20
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := history.Record(LoadEvent{Account: "primary", Model: "gpt-6-sol", Requests: 1}); err != nil {
				t.Errorf("Record: %v", err)
			}
			if err := history.Record(LoadEvent{Account: "primary", Model: "gpt-6-sol", InputTokens: 100, OutputTokens: 20, InputTokenSource: "upstream"}); err != nil {
				t.Errorf("Record tokens: %v", err)
			}
		}()
	}
	wg.Wait()
	reloaded, err := OpenLoadHistory(path, []string{"primary"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reloaded.Snapshot(nil)
	if snapshot.Accounts[0].Requests7d != workers || snapshot.Accounts[0].InputTokens7d != workers*100 || snapshot.Accounts[0].OutputTokens7d != workers*20 || snapshot.Models[0].Requests != workers {
		t.Fatalf("snapshot after restart = %#v", snapshot)
	}
}

func TestLoadHistoryPrunesOlderThanSevenDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 9, 23, 22, 0, 0, 0, time.UTC)
	history, err := OpenLoadHistory(path, []string{"primary"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Record(LoadEvent{At: now.Add(-8 * 24 * time.Hour), Account: "primary", Model: "old", Requests: 1}); err != nil {
		t.Fatal(err)
	}
	if err := history.Record(LoadEvent{At: now.Add(-time.Hour), Account: "primary", Model: "new", Requests: 1, InputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot := history.Snapshot(nil)
	if snapshot.Accounts[0].Requests7d != 1 || len(snapshot.Models) != 1 || snapshot.Models[0].Model != "new" {
		t.Fatalf("pruned snapshot = %#v", snapshot)
	}
}
