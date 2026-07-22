package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevSoakExercisesHealthCompletionAndStreaming(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "curl.log")
	fakeCurl := filepath.Join(tempDir, "curl")
	secret := "test-bearer-value"
	fakeCurlSource := `#!/usr/bin/env bash
set -euo pipefail
output_file=""
request_body=""
config_file=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output|--max-time|--write-out|--request|--header)
      if [ "$1" = "--output" ]; then output_file="$2"; fi
      shift 2
      ;;
    --config)
      config_file="$2"
      shift 2
      ;;
    --data)
      request_body="$2"
      shift 2
      ;;
    --silent|--show-error)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
printf '%s|%s\n' "$url" "$request_body" >> "$MOCK_CURL_LOG"
case "$url" in
  */health/live|*/health/ready)
    printf '{"status":"ok"}' > "$output_file"
    ;;
  */v1/chat/completions)
    grep -q 'Authorization: Bearer test-bearer-value' "$config_file"
    if [[ "$request_body" == *'"stream":true'* ]]; then
      printf 'data: {"choices":[{"delta":{"content":"stream soak ok"}}]}\ndata: [DONE]\n' > "$output_file"
    else
      printf '{"choices":[{"message":{"content":"soak ok"}}]}' > "$output_file"
    fi
    ;;
  *)
    exit 22
    ;;
esac
printf 200
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlSource), 0o700); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}

	apiURL := "https://secret-host.example.invalid"

	cmd := exec.Command("bash", "./dev-soak.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+tempDir+":"+os.Getenv("PATH"),
		"MOCK_CURL_LOG="+logPath,
		"API_URL="+apiURL,
		"GATEWAY_BEARER_SECRET="+secret,
		"SOAK_ITERATIONS=2",
		"SOAK_INTERVAL_SECONDS=0",
		"SOAK_LABEL=test",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dev-soak.sh error = %v\n%s", err, output)
	}
	requestLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake curl log: %v", err)
	}
	requests := string(requestLog)
	if got := strings.Count(requests, "/health/live|"); got != 1 {
		t.Fatalf("live requests = %d, want 1\n%s", got, requests)
	}
	if got := strings.Count(requests, "/health/ready|"); got != 3 {
		t.Fatalf("ready requests = %d, want 3\n%s", got, requests)
	}
	if got := strings.Count(requests, `"stream":true`); got != 2 {
		t.Fatalf("stream requests = %d, want 2\n%s", got, requests)
	}
	if got := strings.Count(requests, "/v1/chat/completions|") - 2; got != 2 {
		t.Fatalf("completion requests = %d, want 2\n%s", got, requests)
	}
	text := string(output)
	for _, want := range []string{"soak_preflight_passed", "iterations=2", "completion_passes=2", "stream_passes=2", "failures=0", "soak_passed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{apiURL, secret} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("output leaked %q:\n%s", forbidden, text)
		}
	}
}
