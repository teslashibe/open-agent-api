package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
	"github.com/teslashibe/open-agent-api/internal/structured"
)

const contractFixtureDir = "../structured/testdata/contract-2.0.0"

func TestContract200GoldenManifest(t *testing.T) {
	raw := readContractFixture(t, "SHA256SUMS")
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("manifest entries = %d, want 3", len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid manifest line %q", line)
		}
		name := filepath.Base(fields[1])
		if seen[name] {
			t.Fatalf("duplicate manifest entry %q", name)
		}
		seen[name] = true
		sum := sha256.Sum256(readContractFixture(t, name))
		if got := fmt.Sprintf("%x", sum); got != fields[0] {
			t.Fatalf("%s digest = %s, want %s", name, got, fields[0])
		}
	}
	for _, name := range []string{"request.json", "success.json", "error.json"} {
		if !seen[name] {
			t.Fatalf("manifest missing %s", name)
		}
	}
}

func TestContract200GoldenRequestAndSuccess(t *testing.T) {
	if StructuredPath != "/v1/structured/inference" {
		t.Fatalf("StructuredPath = %q", StructuredPath)
	}
	if structured.ContractVersion != "2.0.0" {
		t.Fatalf("ContractVersion = %q", structured.ContractVersion)
	}

	raw := readContractFixture(t, "request.json")
	var wire map[string]json.RawMessage
	decodeStrictJSON(t, raw, &wire)
	for _, forbidden := range []string{"max_output_tokens", "contract_version", "reasoning", "schema_wrapper"} {
		if _, ok := wire[forbidden]; ok {
			t.Fatalf("request fixture contains forbidden field %q", forbidden)
		}
	}
	var request structured.Request
	decodeStrictJSON(t, raw, &request)

	app := New(
		structuredTestConfig(),
		WithCodexService(fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
			return codex.Completion{
				Text:  structuredTestOutput,
				Model: openai.DefaultModel,
				ID:    "resp-upstream-1",
				Usage: openai.Usage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16},
			}, nil
		}}),
		WithLogOutput(nil),
	)
	httpRequest, err := http.NewRequest(http.MethodPost, StructuredPath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer secret-access-token")
	response, err := app.Test(httpRequest, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	var actual structured.Response
	decodeStrictReader(t, response.Body, &actual)
	var golden structured.Response
	decodeStrictJSON(t, readContractFixture(t, "success.json"), &golden)

	// Latency and build provenance vary by process. Everything else is stable.
	actual.LatencyMS, golden.LatencyMS = 0, 0
	actual.Build = golden.Build
	actualData, goldenData := any(nil), any(nil)
	if err := json.Unmarshal(actual.Data, &actualData); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(golden.Data, &goldenData); err != nil {
		t.Fatal(err)
	}
	actual.Data, golden.Data = nil, nil
	if !reflect.DeepEqual(actual, golden) {
		t.Fatalf("stable success fields differ:\nactual: %#v\ngolden: %#v", actual, golden)
	}
	if !reflect.DeepEqual(actualData, goldenData) {
		t.Fatalf("response data differs:\nactual: %#v\ngolden: %#v", actualData, goldenData)
	}
}

func TestContract200GoldenErrorIsHTTP409IdempotencyConflict(t *testing.T) {
	var golden structured.ErrorResponse
	decodeStrictJSON(t, readContractFixture(t, "error.json"), &golden)
	if golden.ContractVersion != "2.0.0" {
		t.Fatalf("contract_version = %q", golden.ContractVersion)
	}
	contractErr := structured.NewError(golden.Error.Code, golden.Error.Message, golden.Error.Details...)
	_, status, _ := structuredFailure(contractErr)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if golden.Error.Code != structured.CodeIdempotencyConflict {
		t.Fatalf("code = %q", golden.Error.Code)
	}
}

func readContractFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeStrictJSON(t *testing.T, raw []byte, target any) {
	t.Helper()
	decodeStrictReader(t, bytes.NewReader(raw), target)
}

func decodeStrictReader(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("strict decode: %v", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatal("fixture contains trailing JSON")
	}
}
