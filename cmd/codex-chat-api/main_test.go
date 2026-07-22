package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/config"
	metricspkg "github.com/teslashibe/codex-chat-api/internal/metrics"
)

func TestBuildCodexServiceFailsFastWithRedactedDiagnostics(t *testing.T) {
	tests := []struct {
		name      string
		want      string
		configure func(t *testing.T, client *config.CodexClient)
	}{
		{
			name: "unreadable auth",
			want: "credential file unreadable",
			configure: func(t *testing.T, client *config.CodexClient) {
				client.AuthPath = filepath.Join(t.TempDir(), "raw-user@example.test-secret-access-token-auth.json")
			},
		},
		{
			name: "invalid auth JSON",
			want: "credential file contains invalid JSON",
			configure: func(t *testing.T, client *config.CodexClient) {
				writeStartupFixture(t, client.AuthPath, `{"tokens":{"access_token":"secret-access-token raw-user@example.test"`)
			},
		},
		{
			name: "invalid auth schema",
			want: "credential file schema invalid: missing tokens.account_id",
			configure: func(t *testing.T, client *config.CodexClient) {
				writeStartupFixture(t, client.AuthPath, `{"tokens":{"access_token":"secret-access-token raw-user@example.test"}}`)
			},
		},
		{
			name: "unreadable profile",
			want: "profile file unreadable",
			configure: func(t *testing.T, client *config.CodexClient) {
				client.CodexProfilePath = filepath.Join(t.TempDir(), "raw-user@example.test-secret-access-token-profile.json")
			},
		},
		{
			name: "invalid scaffold JSON",
			want: "scaffold file contains invalid JSON",
			configure: func(t *testing.T, client *config.CodexClient) {
				writeStartupFixture(t, client.CodexScaffoldPath, `{"identity":"raw-user@example.test","token":"secret-access-token"`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			client := config.CodexClient{
				Label:             "work-a",
				CodexHome:         dir,
				AuthPath:          filepath.Join(dir, "auth.json"),
				CodexProfilePath:  filepath.Join(dir, "profile.json"),
				CodexScaffoldPath: filepath.Join(dir, "scaffold.json"),
			}
			writeStartupFixture(t, client.AuthPath, `{"tokens":{"access_token":"valid-token","account_id":"acct-safe"}}`)
			writeStartupFixture(t, client.CodexProfilePath, `{}`)
			writeStartupFixture(t, client.CodexScaffoldPath, `{}`)
			tt.configure(t, &client)

			cfg := config.Defaults()
			cfg.CodexClients = []config.CodexClient{client}
			_, err := buildCodexService(cfg, metricspkg.New(false))
			if err == nil {
				t.Fatal("buildCodexService() error = nil, want startup validation failure")
			}
			message := err.Error()
			for _, want := range []string{`create codex client "work-a"`, tt.want} {
				if !strings.Contains(message, want) {
					t.Fatalf("startup error missing %q: %s", want, message)
				}
			}
			for _, forbidden := range []string{
				client.AuthPath,
				client.CodexProfilePath,
				client.CodexScaffoldPath,
				"secret-access-token",
				"raw-user@example.test",
				"acct-safe",
			} {
				if strings.Contains(message, forbidden) {
					t.Fatalf("startup error leaked %q: %s", forbidden, message)
				}
			}
		})
	}
}

func writeStartupFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
