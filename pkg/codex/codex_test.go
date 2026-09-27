package codex

import (
	"context"
	"errors"
	"fmt"
	"testing"

	internal "github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

type fakeService struct {
	req        internal.Request
	completion internal.Completion
	err        error
}

func (f *fakeService) Complete(_ context.Context, req internal.Request) (internal.Completion, error) {
	f.req = req
	return f.completion, f.err
}

func (f *fakeService) Stream(context.Context, internal.Request) (<-chan internal.StreamEvent, error) {
	return nil, errors.New("not used")
}

func TestCompleteResolvesAliasLikeTheGateway(t *testing.T) {
	fake := &fakeService{completion: internal.Completion{Text: "hi", Model: "gpt-5.6-luna", Usage: openai.Usage{PromptTokens: 12, CompletionTokens: 3}}}
	c := &Client{service: fake}
	got, err := c.Complete(context.Background(), "gpt-5.6-luna-fast-low", "say hi")
	if err != nil {
		t.Fatal(err)
	}
	alias := openai.ResolveModelAlias("gpt-5.6-luna-fast-low")
	if fake.req.Model != "gpt-5.6-luna" || fake.req.ReasoningEffort != alias.ReasoningEffort || fake.req.ServiceTier != alias.ServiceTier || alias.ServiceTier == "" || !fake.req.Faithful || !fake.req.Prewarm {
		t.Fatalf("request %+v for alias %+v", fake.req, alias)
	}
	if len(fake.req.Messages) != 1 || fake.req.Messages[0].Role != "user" || string(fake.req.Messages[0].Content) != `"say hi"` {
		t.Fatalf("messages %+v", fake.req.Messages)
	}
	if got != (Result{Text: "hi", Model: "gpt-5.6-luna", Usage: true, InputTokens: 12, OutputTokens: 3}) {
		t.Fatalf("result %+v", got)
	}
}

func TestCompleteReportsMissingUsage(t *testing.T) {
	c := &Client{service: &fakeService{completion: internal.Completion{Text: "hi", Model: "gpt-5.6-terra"}}}
	got, err := c.Complete(context.Background(), "gpt-5.6-terra", "x")
	if err != nil || got.Usage || got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestIsCapacity(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("wrapped: %w", internal.ErrUsageLimitReached), true},
		{internal.NewError(internal.ErrorKindUpstream, 429, "rate limited", nil), true},
		{internal.NewError(internal.ErrorKindUpstream, 503, "no account", nil), true},
		{internal.NewError(internal.ErrorKindClient, 400, "bad request", nil), false},
		{errors.New("other"), false},
	} {
		if got := IsCapacity(tc.err); got != tc.want {
			t.Errorf("%v: got %v", tc.err, got)
		}
	}
}

func TestNewRequiresAccount(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("accepted no accounts")
	}
}
