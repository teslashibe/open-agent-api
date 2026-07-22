package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codextest"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

const soakCredentialCount = 32

type soakCredential struct {
	label     string
	accountID string
	authPath  string
}

type soakJob struct {
	req      Request
	stream   bool
	wantText string
}

type soakResult struct {
	text string
	err  error
}

// TestMultiCredentialLoadAndFailureSoak is a bounded, deterministic harness:
// every credential and upstream response is synthetic, and the only socket is
// a loopback fake owned by the test process.
func TestMultiCredentialLoadAndFailureSoak(t *testing.T) {
	requireSoakLoopback(t)

	t.Run("distribution_and_sticky_affinity", testSoakDistributionAndAffinity)
	t.Run("quota_auth_and_restart", testSoakQuotaAuthAndRestart)
	t.Run("saturation_cancellation_and_disconnect", testSoakSaturationCancellationAndDisconnect)
	t.Run("drain_active_streams_and_restart", testSoakDrainAndRestart)
}

func testSoakDistributionAndAffinity(t *testing.T) {
	routes := make(map[string][]codextest.Script, soakCredentialCount)
	for index := range soakCredentialCount {
		accountID := soakAccountID(index)
		for range 4 {
			routes[accountID] = append(routes[accountID], soakSuccessScript(accountID))
		}
	}
	upstream := newAccountRoutedUpstream(routes)
	defer upstream.Close()

	clients, credentials := newSoakClients(t, upstream.URL(), soakCredentialCount)
	pool := newSoakPool(t, clients, 2, 250*time.Millisecond, nil)
	jobs := make([]soakJob, 0, soakCredentialCount*2)
	for index, credential := range credentials {
		for ordinal := range 2 {
			req := soakRequestForIndex(pool, index, fmt.Sprintf("load-%02d-%d", index, ordinal))
			jobs = append(jobs, soakJob{
				req:      req,
				stream:   ordinal%2 == 0,
				wantText: credential.accountID,
			})
		}
	}

	for wave := range 2 {
		waveJobs := append([]soakJob(nil), jobs...)
		for index := range waveJobs {
			waveJobs[index].req.RequestID = fmt.Sprintf("soak-load-%d-%03d", wave, index)
		}
		results := runSoakWave(pool, waveJobs)
		for index, result := range results {
			if result.err != nil || result.text != waveJobs[index].wantText {
				t.Fatalf("wave %d job %d = text %q, error %v; want %q", wave, index, result.text, result.err, waveJobs[index].wantText)
			}
		}
		assertSoakPoolIdle(t, pool)
		assertSoakUpstreamIdle(t, upstream)
	}

	counts := soakRequestCounts(upstream.Requests())
	if len(counts) != soakCredentialCount {
		t.Fatalf("credential distribution = %d accounts, want %d; counts=%v", len(counts), soakCredentialCount, counts)
	}
	for _, credential := range credentials {
		if got := counts[credential.accountID]; got != 4 {
			t.Fatalf("requests for %s = %d, want 4 sticky requests", credential.label, got)
		}
	}
}

func testSoakQuotaAuthAndRestart(t *testing.T) {
	now := time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC)
	upstream := newAccountRoutedUpstream(map[string][]codextest.Script{
		soakAccountID(0): {soakQuotaScript(2*time.Second, now.Add(3*time.Second)), soakSuccessScript(soakAccountID(0))},
		soakAccountID(1): {soakSuccessScript(soakAccountID(1)), soakSuccessScript(soakAccountID(1))},
		soakAccountID(2): {soakAuthScript(), soakSuccessScript(soakAccountID(2))},
		soakAccountID(3): {soakSuccessScript(soakAccountID(3)), soakSuccessScript(soakAccountID(3))},
	})
	defer upstream.Close()

	clients, credentials := newSoakClients(t, upstream.URL(), 4)
	pool := newSoakPool(t, clients, 2, 100*time.Millisecond, func() time.Time { return now })

	quotaReq := soakRequestForIndex(pool, 0, "quota-sticky")
	if completion, err := pool.Complete(context.Background(), quotaReq); err != nil || completion.Text != soakAccountID(1) {
		t.Fatalf("quota rotation = %#v, %v; want account 1", completion, err)
	}
	if completion, err := pool.Complete(context.Background(), quotaReq); err != nil || completion.Text != soakAccountID(1) {
		t.Fatalf("quota replacement affinity = %#v, %v; want account 1", completion, err)
	}
	pool.mu.Lock()
	cooldown := pool.cooldowns[0]
	pool.mu.Unlock()
	if cooldown.class != FailureQuota || !cooldown.until.Equal(now.Add(3*time.Second)) {
		t.Fatalf("quota cooldown = class %q until %s, want later reset hint %s", cooldown.class, cooldown.until, now.Add(3*time.Second))
	}

	authReq := soakRequestForIndex(pool, 2, "auth-sticky")
	if completion, err := pool.Complete(context.Background(), authReq); err != nil || completion.Text != soakAccountID(3) {
		t.Fatalf("auth rotation = %#v, %v; want account 3", completion, err)
	}
	if health := pool.Health(); health.UsableClients != 3 || health.Clients[2].Reason != clientHealthReasonAuth {
		t.Fatalf("health after auth expiry = %#v, want account 2 unhealthy", health)
	}
	if completion, err := pool.Complete(context.Background(), authReq); err != nil || completion.Text != soakAccountID(3) {
		t.Fatalf("auth replacement affinity = %#v, %v; want account 3", completion, err)
	}

	writeSoakAuth(t, credentials[2].authPath, "replacement-token-02", credentials[2].accountID)
	if health := pool.Health(); health.UsableClients != 4 || health.Clients[2].Status != "usable" {
		t.Fatalf("health after credential replacement = %#v, want all usable", health)
	}
	recoveredReq := soakRequestForIndex(pool, 2, "auth-recovered-new-key")
	if completion, err := pool.Complete(context.Background(), recoveredReq); err != nil || completion.Text != soakAccountID(2) {
		t.Fatalf("recovered credential request = %#v, %v; want account 2", completion, err)
	}
	assertSoakPoolIdle(t, pool)
	assertSoakUpstreamIdle(t, upstream)

	restarted := newSoakPool(t, clients, 2, 100*time.Millisecond, func() time.Time { return now })
	restartReq := soakRequestForIndex(restarted, 0, "restart-clears-cooldown")
	if completion, err := restarted.Complete(context.Background(), restartReq); err != nil || completion.Text != soakAccountID(0) {
		t.Fatalf("restart request = %#v, %v; want cooled account restored", completion, err)
	}
	assertSoakPoolIdle(t, restarted)
	assertSoakUpstreamIdle(t, upstream)

	counts := soakRequestCounts(upstream.Requests())
	for accountID, want := range map[string]int{
		soakAccountID(0): 2,
		soakAccountID(1): 2,
		soakAccountID(2): 2,
		soakAccountID(3): 2,
	} {
		if got := counts[accountID]; got != want {
			t.Fatalf("requests for %s = %d, want %d; counts=%v", accountID, got, want, counts)
		}
	}
}

func testSoakSaturationCancellationAndDisconnect(t *testing.T) {
	gateA := make(chan struct{})
	gateB := make(chan struct{})
	upstream := newAccountRoutedUpstream(map[string][]codextest.Script{
		soakAccountID(0): {
			soakGatedSuccessScript(gateA, soakAccountID(0)),
			{codextest.DisconnectFrame()},
			soakSuccessScript(soakAccountID(0)),
		},
		soakAccountID(1): {soakGatedSuccessScript(gateB, soakAccountID(1))},
	})
	defer upstream.Close()

	clients, _ := newSoakClients(t, upstream.URL(), 2)
	pool := newSoakPool(t, clients, 1, 80*time.Millisecond, nil)
	req := soakRequestForIndex(pool, 0, "saturation")

	first, err := pool.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	second, err := pool.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("saturation rotation Stream() error = %v", err)
	}
	if !upstream.WaitRequests(2, time.Second) {
		t.Fatalf("upstream requests = %d, want two active streams", len(upstream.Requests()))
	}
	assertSoakInflight(t, pool, map[string]int{"synthetic-00": 1, "synthetic-01": 1})

	var nowCalls atomic.Int32
	reachedWait := make(chan struct{})
	resumeScan := make(chan struct{})
	pool.now = func() time.Time {
		if nowCalls.Add(1) == 3 { // preferredIndex plus both saturated clients
			close(reachedWait)
			<-resumeScan
		}
		return time.Now()
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelResult := make(chan error, 1)
	go func() {
		_, err := pool.Stream(cancelCtx, req)
		cancelResult <- err
	}()
	select {
	case <-reachedWait:
	case <-time.After(time.Second):
		t.Fatal("cancelable acquisition did not scan saturated clients")
	}
	cancel()
	close(resumeScan)
	select {
	case err := <-cancelResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("saturation cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("saturated acquisition did not exit after cancellation")
	}
	pool.now = time.Now
	if _, err := pool.Stream(context.Background(), req); !errors.Is(err, ErrClientPoolSaturated) {
		t.Fatalf("bounded saturation error = %v, want ErrClientPoolSaturated", err)
	}
	if got := len(upstream.Requests()); got != 2 {
		t.Fatalf("upstream requests while saturated = %d, want 2", got)
	}

	close(gateA)
	close(gateB)
	if text, err := soakStreamText(first); err != nil || text != soakAccountID(0) {
		t.Fatalf("first active stream = %q, %v", text, err)
	}
	if text, err := soakStreamText(second); err != nil || text != soakAccountID(1) {
		t.Fatalf("rotated active stream = %q, %v", text, err)
	}
	assertSoakPoolIdle(t, pool)
	assertSoakUpstreamIdle(t, upstream)

	if _, err := pool.Complete(context.Background(), req); err == nil || ClassifyFailure(err) != FailureTransient {
		t.Fatalf("disconnect error = %v, class %q; want transient failure", err, ClassifyFailure(err))
	}
	assertSoakPoolIdle(t, pool)
	if completion, err := pool.Complete(context.Background(), req); err != nil || completion.Text != soakAccountID(0) {
		t.Fatalf("post-disconnect lease reuse = %#v, %v", completion, err)
	}
	assertSoakPoolIdle(t, pool)
	assertSoakUpstreamIdle(t, upstream)

	counts := soakRequestCounts(upstream.Requests())
	if counts[soakAccountID(0)] != 3 || counts[soakAccountID(1)] != 1 {
		t.Fatalf("disconnect rotation was not bounded: counts=%v", counts)
	}
}

func testSoakDrainAndRestart(t *testing.T) {
	const clientCount = 4
	routes := make(map[string][]codextest.Script, clientCount)
	gates := make([]chan struct{}, clientCount)
	for index := range clientCount {
		gates[index] = make(chan struct{})
		accountID := soakAccountID(index)
		routes[accountID] = []codextest.Script{
			soakGatedSuccessScript(gates[index], accountID),
			soakSuccessScript(accountID),
		}
	}
	upstream := newAccountRoutedUpstream(routes)
	defer upstream.Close()

	clients, _ := newSoakClients(t, upstream.URL(), clientCount)
	pool := newSoakPool(t, clients, 1, time.Second, nil)
	active := make([]<-chan StreamEvent, clientCount)
	for index := range clientCount {
		events, err := pool.Stream(context.Background(), soakRequestForIndex(pool, index, fmt.Sprintf("drain-active-%d", index)))
		if err != nil {
			t.Fatalf("active Stream(%d) error = %v", index, err)
		}
		active[index] = events
	}
	if !upstream.WaitRequests(clientCount, time.Second) {
		t.Fatalf("upstream requests = %d, want %d active streams", len(upstream.Requests()), clientCount)
	}

	var nowCalls atomic.Int32
	reachedWait := make(chan struct{})
	resumeScan := make(chan struct{})
	pool.now = func() time.Time {
		if nowCalls.Add(1) == clientCount+1 {
			close(reachedWait)
			<-resumeScan
		}
		return time.Now()
	}
	waitResult := make(chan error, 1)
	go func() {
		_, err := pool.Stream(context.Background(), soakRequestForIndex(pool, 0, "drain-waiter"))
		waitResult <- err
	}()
	select {
	case <-reachedWait:
	case <-time.After(time.Second):
		t.Fatal("pending acquisition did not scan saturated clients")
	}
	pool.SetDraining(true)
	close(resumeScan)
	select {
	case err := <-waitResult:
		if !errors.Is(err, ErrClientPoolDraining) {
			t.Fatalf("pending drain error = %v, want ErrClientPoolDraining", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending acquisition did not exit during drain")
	}
	if _, err := pool.Complete(context.Background(), soakRequestForIndex(pool, 1, "drain-new")); !errors.Is(err, ErrClientPoolDraining) {
		t.Fatalf("new work while draining error = %v, want ErrClientPoolDraining", err)
	}
	if got := len(upstream.Requests()); got != clientCount {
		t.Fatalf("upstream requests after drain rejection = %d, want %d", got, clientCount)
	}

	for _, gate := range gates {
		close(gate)
	}
	for index, events := range active {
		text, err := soakStreamText(events)
		if err != nil || text != soakAccountID(index) {
			t.Fatalf("active stream %d during drain = %q, %v", index, text, err)
		}
	}
	assertSoakPoolIdle(t, pool)
	assertSoakUpstreamIdle(t, upstream)

	restarted := newSoakPool(t, clients, 1, 100*time.Millisecond, nil)
	restartReq := soakRequestForIndex(restarted, 2, "drain-restart")
	if completion, err := restarted.Complete(context.Background(), restartReq); err != nil || completion.Text != soakAccountID(2) {
		t.Fatalf("post-drain restart = %#v, %v; want new work accepted", completion, err)
	}
	assertSoakPoolIdle(t, restarted)
	assertSoakUpstreamIdle(t, upstream)
}

func newAccountRoutedUpstream(scripts map[string][]codextest.Script) *codextest.Upstream {
	return codextest.NewRoutedUpstream(
		func(req codextest.Request) string { return req.Headers.Get("Chatgpt-Account-Id") },
		scripts,
	)
}

func newSoakClients(t *testing.T, upstreamURL string, count int) ([]PooledClientConfig, []soakCredential) {
	t.Helper()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.json")
	scaffoldPath := filepath.Join(dir, "scaffold.json")
	writeSoakFile(t, profilePath, `{}`)
	writeSoakFile(t, scaffoldPath, `{}`)

	clients := make([]PooledClientConfig, 0, count)
	credentials := make([]soakCredential, 0, count)
	for index := range count {
		label := fmt.Sprintf("synthetic-%02d", index)
		accountID := soakAccountID(index)
		codexHome := filepath.Join(dir, label)
		if err := os.Mkdir(codexHome, 0o700); err != nil {
			t.Fatalf("create synthetic codex home: %v", err)
		}
		authPath := filepath.Join(codexHome, "auth.json")
		writeSoakAuth(t, authPath, fmt.Sprintf("synthetic-token-%02d", index), accountID)
		writeSoakFile(t, filepath.Join(codexHome, "installation_id"), fmt.Sprintf("synthetic-install-%02d\n", index))
		client, err := NewClient(ClientConfig{
			AuthPath:     authPath,
			CodexHome:    codexHome,
			ProfilePath:  profilePath,
			ScaffoldPath: scaffoldPath,
			WebsocketURL: upstreamURL,
			Timeout:      3 * time.Second,
			LogOutput:    io.Discard,
		})
		if err != nil {
			t.Fatalf("create synthetic client %s: %v", label, err)
		}
		clients = append(clients, PooledClientConfig{Label: label, Service: client})
		credentials = append(credentials, soakCredential{label: label, accountID: accountID, authPath: authPath})
	}
	return clients, credentials
}

func newSoakPool(t *testing.T, clients []PooledClientConfig, maxInflight int, waitTimeout time.Duration, now func() time.Time) *PooledService {
	t.Helper()
	pool, err := NewPooledService(PooledServiceConfig{
		Clients:           clients,
		MaxInflight:       maxInflight,
		UnavailablePolicy: ClientPoolUnavailableFail,
		LogOutput:         io.Discard,
		WaitTimeout:       waitTimeout,
		CooldownDefault:   time.Minute,
		Now:               now,
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	return pool
}

func runSoakWave(pool *PooledService, jobs []soakJob) []soakResult {
	results := make([]soakResult, len(jobs))
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(len(jobs))
	for index, job := range jobs {
		index, job := index, job
		go func() {
			defer workers.Done()
			<-start
			if job.stream {
				events, err := pool.Stream(context.Background(), job.req)
				if err != nil {
					results[index].err = err
					return
				}
				results[index].text, results[index].err = soakStreamText(events)
				return
			}
			completion, err := pool.Complete(context.Background(), job.req)
			results[index] = soakResult{text: completion.Text, err: err}
		}()
	}
	close(start)
	workers.Wait()
	return results
}

func soakStreamText(events <-chan StreamEvent) (string, error) {
	text := ""
	for event := range events {
		if event.Err != nil {
			return text, event.Err
		}
		text += event.Delta
	}
	return text, nil
}

func soakRequestForIndex(pool *PooledService, want int, name string) Request {
	req := Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent(name)}},
		AffinityKey:     "soak:" + name,
		AffinityKeyHash: "synthetic-hash",
		AffinityKeyMode: "test",
		RequestID:       "soak-" + name,
		ReasoningEffort: "medium",
		Verbosity:       "medium",
	}
	for pool.selectIndex(req) != want {
		req.AffinityKey += "-next"
	}
	return req
}

func soakSuccessScript(text string) codextest.Script {
	return codextest.Script{
		codextest.TextFrame(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, text)),
		codextest.TextFrame(`{"type":"response.completed","response":{"id":"soak-response","model":"gpt-test"}}`),
	}
}

func soakGatedSuccessScript(gate <-chan struct{}, text string) codextest.Script {
	return codextest.Script{
		codextest.GatedFrame(gate, fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, text)),
		codextest.TextFrame(`{"type":"response.completed","response":{"id":"soak-response","model":"gpt-test"}}`),
	}
}

func soakQuotaScript(retryAfter time.Duration, resetAt time.Time) codextest.Script {
	return codextest.Script{codextest.TextFrame(fmt.Sprintf(
		`{"type":"response.failed","status":429,"error":{"code":"usage_limit_reached","retry_after":%d,"resets_at":%q}}`,
		int64(retryAfter/time.Second),
		resetAt.Format(time.RFC3339Nano),
	))}
}

func soakAuthScript() codextest.Script {
	return codextest.Script{codextest.TextFrame(`{"type":"response.failed","status":401,"error":{"code":"authentication_expired"}}`)}
}

func soakAccountID(index int) string {
	return fmt.Sprintf("synthetic-account-%02d", index)
}

func soakRequestCounts(requests []codextest.Request) map[string]int {
	counts := make(map[string]int)
	for _, req := range requests {
		counts[req.Headers.Get("Chatgpt-Account-Id")]++
	}
	return counts
}

func assertSoakInflight(t *testing.T, pool *PooledService, want map[string]int) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for label, count := range want {
		if got := pool.inflight[label]; got != count {
			t.Fatalf("inflight[%s] = %d, want %d; all=%v", label, got, count, pool.inflight)
		}
	}
}

func assertSoakPoolIdle(t *testing.T, pools ...*PooledService) {
	t.Helper()
	for _, pool := range pools {
		pool.mu.Lock()
		for label, inflight := range pool.inflight {
			if inflight != 0 {
				pool.mu.Unlock()
				t.Fatalf("pool lease leak: inflight[%s]=%d", label, inflight)
			}
		}
		pool.mu.Unlock()
	}
}

func assertSoakUpstreamIdle(t *testing.T, upstream *codextest.Upstream) {
	t.Helper()
	if !upstream.WaitIdle(time.Second) {
		t.Fatalf("fake upstream connection leak: active=%d", upstream.ActiveConnections())
	}
}

func writeSoakAuth(t *testing.T, path, token, accountID string) {
	t.Helper()
	writeSoakFile(t, path, fmt.Sprintf(`{"tokens":{"access_token":%q,"account_id":%q}}`, token, accountID))
}

func writeSoakFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write synthetic fixture: %v", err)
	}
}

func requireSoakLoopback(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback sockets unavailable: %v", err)
	}
	_ = listener.Close()
}
