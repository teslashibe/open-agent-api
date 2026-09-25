package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const loadHistoryWindow = 7 * 24 * time.Hour

// LoadHistory stores bounded request and token aggregates, not prompt content
// or conversation identifiers. The JSON file is replaced atomically on flush.
type LoadHistory struct {
	mu       sync.Mutex
	path     string
	now      func() time.Time
	events   []LoadEvent
	accounts []string
}

type LoadEvent struct {
	At               time.Time `json:"at"`
	Account          string    `json:"account"`
	Model            string    `json:"model"`
	Requests         int64     `json:"requests"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	InputTokenSource string    `json:"input_token_source,omitempty"`
}

type LoadHistorySnapshot struct {
	ObservedAt time.Time        `json:"observed_at"`
	Window     string           `json:"window"`
	Accounts   []AccountLoad    `json:"accounts"`
	Models     []ModelTokenLoad `json:"models"`
}

type AccountLoad struct {
	Label          string `json:"label"`
	InFlight       int    `json:"in_flight"`
	Requests1h     int64  `json:"requests_1h"`
	Requests24h    int64  `json:"requests_24h"`
	Requests7d     int64  `json:"requests_7d"`
	InputTokens7d  int64  `json:"input_tokens_7d"`
	OutputTokens7d int64  `json:"output_tokens_7d"`
}

type ModelTokenLoad struct {
	Account      string `json:"account"`
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

type loadHistoryFile struct {
	Version int         `json:"version"`
	Events  []LoadEvent `json:"events"`
}

func OpenLoadHistory(path string, accounts []string, now func() time.Time) (*LoadHistory, error) {
	if path == "" {
		return nil, fmt.Errorf("load history path is required")
	}
	if now == nil {
		now = time.Now
	}
	history := &LoadHistory{path: path, now: now, accounts: append([]string(nil), accounts...)}
	file, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("open load history: %w", err)
	}
	if err == nil {
		defer file.Close()
		var saved loadHistoryFile
		if err := json.NewDecoder(file).Decode(&saved); err != nil {
			return nil, fmt.Errorf("decode load history: %w", err)
		}
		if saved.Version != 1 {
			return nil, fmt.Errorf("unsupported load history version %d", saved.Version)
		}
		history.events = saved.Events
	}
	history.pruneLocked(now())
	if err := history.flushLocked(); err != nil {
		return nil, err
	}
	return history, nil
}

func (h *LoadHistory) Record(event LoadEvent) error {
	if event.Account == "" {
		return fmt.Errorf("load event account is required")
	}
	if event.At.IsZero() {
		event.At = h.now().UTC()
	}
	if event.At.After(h.now().Add(time.Minute)) {
		return fmt.Errorf("load event timestamp must not be in the future")
	}
	if event.Requests < 0 || event.InputTokens < 0 || event.OutputTokens < 0 {
		return fmt.Errorf("load event counts must be nonnegative")
	}
	if event.Model == "" {
		event.Model = "unknown"
	}
	event.At = event.At.UTC().Truncate(time.Minute)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(h.now())
	for i := range h.events {
		stored := &h.events[i]
		if stored.At.Equal(event.At) && stored.Account == event.Account && stored.Model == event.Model && stored.InputTokenSource == event.InputTokenSource {
			previous := *stored
			stored.Requests += event.Requests
			stored.InputTokens += event.InputTokens
			stored.OutputTokens += event.OutputTokens
			if err := h.flushLocked(); err != nil {
				*stored = previous
				return err
			}
			return nil
		}
	}
	h.events = append(h.events, event)
	if err := h.flushLocked(); err != nil {
		h.events = h.events[:len(h.events)-1]
		return err
	}
	return nil
}

func (h *LoadHistory) Snapshot(inflight map[string]int) LoadHistorySnapshot {
	now := h.now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(now)
	accounts := make(map[string]*AccountLoad, len(h.accounts))
	models := make(map[string]*ModelTokenLoad)
	for _, label := range h.accounts {
		accounts[label] = &AccountLoad{Label: label, InFlight: inflight[label]}
	}
	for _, event := range h.events {
		account := accounts[event.Account]
		if account == nil {
			account = &AccountLoad{Label: event.Account, InFlight: inflight[event.Account]}
			accounts[event.Account] = account
		}
		if event.At.After(now.Add(-time.Hour)) {
			account.Requests1h += event.Requests
		}
		if event.At.After(now.Add(-24 * time.Hour)) {
			account.Requests24h += event.Requests
		}
		account.Requests7d += event.Requests
		account.InputTokens7d += event.InputTokens
		account.OutputTokens7d += event.OutputTokens
		key := event.Account + "\x00" + event.Model
		model := models[key]
		if model == nil {
			model = &ModelTokenLoad{Account: event.Account, Model: event.Model}
			models[key] = model
		}
		model.Requests += event.Requests
		model.InputTokens += event.InputTokens
		model.OutputTokens += event.OutputTokens
	}
	out := LoadHistorySnapshot{ObservedAt: now, Window: "7d", Accounts: make([]AccountLoad, 0, len(accounts)), Models: make([]ModelTokenLoad, 0, len(models))}
	for _, item := range accounts {
		out.Accounts = append(out.Accounts, *item)
	}
	for _, item := range models {
		out.Models = append(out.Models, *item)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].Label < out.Accounts[j].Label })
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Account == out.Models[j].Account {
			return out.Models[i].Model < out.Models[j].Model
		}
		return out.Models[i].Account < out.Models[j].Account
	})
	return out
}

func (h *LoadHistory) pruneLocked(now time.Time) {
	cutoff := now.Add(-loadHistoryWindow)
	kept := h.events[:0]
	for _, event := range h.events {
		if !event.At.After(cutoff) {
			continue
		}
		if event.At.After(now.Add(time.Minute)) {
			continue
		}
		if event.Requests < 0 || event.InputTokens < 0 || event.OutputTokens < 0 {
			continue
		}
		kept = append(kept, event)
	}
	h.events = kept
}

func (h *LoadHistory) flushLocked() error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return fmt.Errorf("create load history directory: %w", err)
	}
	data, err := json.Marshal(loadHistoryFile{Version: 1, Events: h.events})
	if err != nil {
		return fmt.Errorf("encode load history: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(h.path), ".load-history-*.tmp")
	if err != nil {
		return fmt.Errorf("create load history checkpoint: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure load history checkpoint: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write load history checkpoint: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync load history checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close load history checkpoint: %w", err)
	}
	if err := os.Rename(tempName, h.path); err != nil {
		return fmt.Errorf("replace load history: %w", err)
	}
	return nil
}
