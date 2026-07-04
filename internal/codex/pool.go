package codex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	ClientPoolUnavailableFail          = "fail"
	ClientPoolUnavailableFallbackFirst = "fallback_first"
)

type PooledService struct {
	clients           []pooledClient
	unavailablePolicy string
	logOutput         io.Writer
}

type pooledClient struct {
	label   string
	service Service
}

type PooledServiceConfig struct {
	Clients           []PooledClientConfig
	UnavailablePolicy string
	LogOutput         io.Writer
}

type PooledClientConfig struct {
	Label   string
	Service Service
}

func NewPooledService(cfg PooledServiceConfig) (*PooledService, error) {
	if len(cfg.Clients) == 0 {
		return nil, fmt.Errorf("at least one codex client is required")
	}
	clients := make([]pooledClient, 0, len(cfg.Clients))
	for i, client := range cfg.Clients {
		if client.Service == nil {
			return nil, fmt.Errorf("codex client %d service is required", i)
		}
		label := client.Label
		if label == "" {
			label = fmt.Sprintf("client-%d", i)
		}
		clients = append(clients, pooledClient{
			label:   label,
			service: client.Service,
		})
	}
	if cfg.UnavailablePolicy == "" {
		cfg.UnavailablePolicy = ClientPoolUnavailableFail
	}
	switch cfg.UnavailablePolicy {
	case ClientPoolUnavailableFail, ClientPoolUnavailableFallbackFirst:
	default:
		return nil, fmt.Errorf("unsupported codex client pool unavailable policy %q", cfg.UnavailablePolicy)
	}
	if cfg.LogOutput == nil {
		cfg.LogOutput = os.Stdout
	}
	logPoolComposition(cfg.LogOutput, clients, cfg.UnavailablePolicy)
	return &PooledService{
		clients:           clients,
		unavailablePolicy: cfg.UnavailablePolicy,
		logOutput:         cfg.LogOutput,
	}, nil
}

// logPoolComposition emits one redacted startup line describing the pool so
// operators can confirm how many Codex client shards are configured and their
// labels. Only the validated non-sensitive labels, count, and policy are
// logged; no auth paths or codex homes are ever printed.
func logPoolComposition(out io.Writer, clients []pooledClient, policy string) {
	if out == nil {
		return
	}
	labels := make([]string, len(clients))
	for i, client := range clients {
		labels[i] = client.label
	}
	fmt.Fprintf(
		out,
		"codex_client_pool clients=%d policy=%s labels=%s\n",
		len(clients),
		policy,
		strings.Join(labels, ","),
	)
}

func (p *PooledService) Complete(ctx context.Context, req Request) (Completion, error) {
	events, err := p.Stream(ctx, req)
	if err != nil {
		return Completion{}, err
	}

	var completion Completion
	for event := range events {
		if event.Err != nil {
			return Completion{}, event.Err
		}
		if event.Delta != "" {
			completion.Text += event.Delta
		}
		if len(event.ToolCalls) > 0 {
			completion.ToolCalls = append(completion.ToolCalls, event.ToolCalls...)
		}
		if event.ToolCallDelta != nil {
			applyToolCallDelta(&completion.ToolCalls, *event.ToolCallDelta)
		}
		if event.ID != "" {
			completion.ID = event.ID
		}
		if event.Model != "" {
			completion.Model = event.Model
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			completion.Usage = event.Usage
		}
	}
	completion.ToolCalls = compactToolCalls(completion.ToolCalls)
	return completion, nil
}

func (p *PooledService) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	index := p.selectIndex(req)
	p.logSelection(req, index, false)
	events, err := p.clients[index].service.Stream(ctx, req)
	if err == nil || !p.shouldFallback(index, err) {
		return events, err
	}
	p.logSelection(req, 0, true)
	return p.clients[0].service.Stream(ctx, req)
}

func (p *PooledService) shouldFallback(index int, err error) bool {
	if p.unavailablePolicy != ClientPoolUnavailableFallbackFirst || index == 0 {
		return false
	}
	if errors.Is(err, ErrClientUnavailable) {
		return true
	}
	if codexErr, ok := ErrorAs(err); ok {
		return codexErr.Kind == ErrorKindAuth || codexErr.Kind == ErrorKindUpstream
	}
	return false
}

func (p *PooledService) selectIndex(req Request) int {
	if len(p.clients) == 1 {
		return 0
	}
	key := req.AffinityKey
	if key == "" {
		key = req.AffinityKeyHash
	}
	if key == "" {
		key = "global"
	}
	sum := sha256.Sum256([]byte(key))
	value := binary.BigEndian.Uint64(sum[:8])
	return int(value % uint64(len(p.clients)))
}

func (p *PooledService) logSelection(req Request, index int, fallback bool) {
	if p.logOutput == nil {
		return
	}
	keyMode := req.AffinityKeyMode
	if keyMode == "" {
		keyMode = "none"
	}
	keyHash := req.AffinityKeyHash
	if keyHash == "" {
		keyHash = "none"
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = "none"
	}
	fmt.Fprintf(
		p.logOutput,
		"codex_client_select request_id=%s key_mode=%s key_hash=%s shard=%d client_label=%s fallback=%t\n",
		requestID,
		keyMode,
		keyHash,
		index,
		p.clients[index].label,
		fallback,
	)
}
