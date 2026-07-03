package codex

import (
	"context"
	"testing"
)

type recordingService struct {
	model string
}

func (s *recordingService) Complete(_ context.Context, req Request) (Completion, error) {
	s.model = req.Model
	return Completion{Model: req.Model}, nil
}

func (s *recordingService) Stream(_ context.Context, req Request) (<-chan StreamEvent, error) {
	s.model = req.Model
	ch := make(chan StreamEvent)
	close(ch)
	return ch, nil
}

func TestRouterPassesArbitraryGeminiModelThrough(t *testing.T) {
	codexSvc := &recordingService{}
	geminiSvc := &recordingService{}
	router := Router{Codex: codexSvc, Gemini: geminiSvc}

	_, err := router.Complete(context.Background(), Request{Model: "gemini-3.1-pro"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if geminiSvc.model != "gemini-3.1-pro" {
		t.Fatalf("gemini model = %q", geminiSvc.model)
	}
	if codexSvc.model != "" {
		t.Fatalf("codex service should not be called, got %q", codexSvc.model)
	}
}

func TestRouterDefaultsNonGeminiToCodex(t *testing.T) {
	codexSvc := &recordingService{}
	geminiSvc := &recordingService{}
	router := Router{Codex: codexSvc, Gemini: geminiSvc}

	_, err := router.Complete(context.Background(), Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if codexSvc.model != "gpt-5.5" {
		t.Fatalf("codex model = %q", codexSvc.model)
	}
	if geminiSvc.model != "" {
		t.Fatalf("gemini service should not be called, got %q", geminiSvc.model)
	}
}

func TestRouterPassesArbitraryClaudeModelThrough(t *testing.T) {
	codexSvc := &recordingService{}
	geminiSvc := &recordingService{}
	claudeSvc := &recordingService{}
	router := Router{Codex: codexSvc, Gemini: geminiSvc, Claude: claudeSvc}

	_, err := router.Complete(context.Background(), Request{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if claudeSvc.model != "claude-sonnet-4-6" {
		t.Fatalf("claude model = %q", claudeSvc.model)
	}
	if codexSvc.model != "" || geminiSvc.model != "" {
		t.Fatalf("unexpected services called codex=%q gemini=%q", codexSvc.model, geminiSvc.model)
	}
}

func TestRouterPassesShortClaudeAliasThrough(t *testing.T) {
	claudeSvc := &recordingService{}
	router := Router{Claude: claudeSvc}

	_, err := router.Complete(context.Background(), Request{Model: "fable"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if claudeSvc.model != "fable" {
		t.Fatalf("claude model = %q", claudeSvc.model)
	}
}

func TestRouterPassesMythosAliasThrough(t *testing.T) {
	claudeSvc := &recordingService{}
	router := Router{Claude: claudeSvc}

	_, err := router.Complete(context.Background(), Request{Model: "mythos"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if claudeSvc.model != "mythos" {
		t.Fatalf("claude model = %q", claudeSvc.model)
	}
}
