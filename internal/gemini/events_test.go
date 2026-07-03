package gemini

import "testing"

func TestParseStreamEventTextThoughtToolAndDone(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{
			name: "text",
			raw:  `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"modelVersion":"gemini-2.5-flash","responseId":"r1"}}`,
			want: 1,
		},
		{
			name: "thought",
			raw:  `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]}}]}}`,
			want: 1,
		},
		{
			name: "tool",
			raw:  `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7},"responseId":"r2"}}`,
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, err := parseStreamEvent([]byte(tc.raw), nil)
			if err != nil {
				t.Fatalf("parseStreamEvent: %v", err)
			}
			if len(events) != tc.want {
				t.Fatalf("events len = %d, want %d", len(events), tc.want)
			}
		})
	}
}

func TestParseStreamEventFunctionCallArgsObject(t *testing.T) {
	events, err := parseStreamEvent([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]}}],"responseId":"abc"}}`), nil)
	if err != nil {
		t.Fatalf("parseStreamEvent: %v", err)
	}
	if events[0].ToolCallDelta == nil {
		t.Fatalf("expected tool call delta")
	}
	if got := events[0].ToolCallDelta.Function.Arguments; got != `{"city":"Paris"}` {
		t.Fatalf("arguments = %s", got)
	}
	if got := events[0].ToolCallDelta.Function.Name; got != "get_weather" {
		t.Fatalf("name = %s", got)
	}
}

func TestParseStreamEventCustomFunctionCall(t *testing.T) {
	events, err := parseStreamEvent([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"apply_patch","args":{"input":"*** Begin Patch\n*** End Patch\n"}}}]}}],"responseId":"abc"}}`), map[string]bool{"apply_patch": true})
	if err != nil {
		t.Fatalf("parseStreamEvent: %v", err)
	}
	if events[0].ToolCallDelta == nil {
		t.Fatalf("expected tool call delta")
	}
	got := events[0].ToolCallDelta
	if got.Type != "custom" || got.Function.Name != "apply_patch" || got.Function.Arguments != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("custom delta = %#v", got)
	}
}
