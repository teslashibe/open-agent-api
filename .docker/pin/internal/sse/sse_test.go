package sse

import "testing"

func TestDataFramesJSON(t *testing.T) {
	got, err := Data(map[string]string{"text": "hello"})
	if err != nil {
		t.Fatalf("Data() error = %v", err)
	}
	want := "data: {\"text\":\"hello\"}\n\n"
	if string(got) != want {
		t.Fatalf("Data() = %q, want %q", got, want)
	}
}

func TestDone(t *testing.T) {
	want := "data: [DONE]\n\n"
	if got := string(Done()); got != want {
		t.Fatalf("Done() = %q, want %q", got, want)
	}
}
