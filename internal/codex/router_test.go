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
