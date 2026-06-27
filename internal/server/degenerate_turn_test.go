package server

import "testing"

func TestDegenerateAgentTurn(t *testing.T) {
	if !degenerateAgentTurn(true, "stop", 200, 0) {
		t.Fatal("expected degenerate agent turn")
	}
	if degenerateAgentTurn(true, "tool_calls", 200, 1) {
		t.Fatal("tool_calls finish should not be degenerate")
	}
	if degenerateAgentTurn(false, "stop", 200, 0) {
		t.Fatal("plain chat should not be degenerate")
	}
	if degenerateAgentTurn(true, "stop", 50, 0) {
		t.Fatal("short text should not be degenerate")
	}
}

func TestDetectLoopPhrase(t *testing.T) {
	if got := detectLoopPhrase("I'll implement that now."); got != "i'll" {
		t.Fatalf("detectLoopPhrase() = %q, want i'll", got)
	}
	if got := detectLoopPhrase("done"); got != "" {
		t.Fatalf("detectLoopPhrase() = %q, want empty", got)
	}
}
