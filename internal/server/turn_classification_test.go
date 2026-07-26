package server

import (
	"encoding/json"
	"testing"

	"github.com/teslashibe/open-chat-api/internal/openai"
)

func TestClassifyTurnShapes(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		toolsPresent bool
		want         turnClass
	}{
		{
			name: "simple no tool user turn",
			body: `{"messages":[{"role":"user","content":"hi"}]}`,
			want: turnClassSimpleNoTool,
		},
		{
			name:         "tool generating turn",
			body:         `{"messages":[{"role":"user","content":"read go.mod"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`,
			toolsPresent: true,
			want:         turnClassToolGenerating,
		},
		{
			name:         "tool result continuation",
			body:         `{"messages":[{"role":"user","content":"read go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_123","content":"module test"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`,
			toolsPresent: true,
			want:         turnClassToolResultContinuation,
		},
		{
			name:         "sequential tool result continuation",
			body:         `{"messages":[{"role":"user","content":"list then read"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_list","type":"function","function":{"name":"list_dir","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_list","content":"go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_read","content":"module test"}],"tools":[{"type":"function","function":{"name":"list_dir"}},{"type":"function","function":{"name":"read_file"}}]}`,
			toolsPresent: true,
			want:         turnClassToolResultContinuation,
		},
		{
			name:         "final prose continuation candidate",
			body:         `{"messages":[{"role":"user","content":"read go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_123","content":"module test"},{"role":"assistant","content":"go.mod declares module test."}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`,
			toolsPresent: true,
			want:         turnClassFinalProseContinuation,
		},
		{
			name:         "unmatched tool result stays tool generating",
			body:         `{"messages":[{"role":"user","content":"read"},{"role":"tool","tool_call_id":"call_missing","content":"result"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`,
			toolsPresent: true,
			want:         turnClassToolGenerating,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ChatCompletionRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			if got := classifyTurn(req, tc.toolsPresent); got != tc.want {
				t.Fatalf("classifyTurn() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAgentQueuePriorityForTurnClass(t *testing.T) {
	if got := agentQueuePriority(turnClassToolResultContinuation); got <= agentQueuePriority(turnClassToolGenerating) {
		t.Fatalf("tool result priority = %d, want greater than tool generating", got)
	}
	if got := agentQueuePriority(turnClassFinalProseContinuation); got <= agentQueuePriority(turnClassToolGenerating) {
		t.Fatalf("final prose priority = %d, want greater than tool generating", got)
	}
}
