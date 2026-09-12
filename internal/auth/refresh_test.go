package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRefreshResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, reason string
	}{
		{"oauth string", 400, `{"error":"invalid_grant","error_description":"private-account-secret"}`, "invalid_grant"},
		{"structured code", 401, `{"error":{"code":"refresh_token_reused","message":"private-account-secret"}}`, "refresh_token_reused"},
		{"structured type", 401, `{"error":{"type":"invalid_grant","message":"private-account-secret"}}`, "invalid_grant"},
		{"unknown code", 400, `{"error":{"code":"private-account-secret"}}`, "unknown_error"},
		{"unknown string", 400, `{"error":"private-account-secret"}`, "unknown_error"},
		{"malformed", 502, `private-account-secret`, "invalid_response"},
		{"unexpected shape", 400, `{"error":["private-account-secret"]}`, "unknown_error"},
		{"missing access token", 200, `{}`, "missing_access_token"},
		{"error despite token", 200, `{"access_token":"private-account-secret","error":"invalid_grant"}`, "invalid_grant"},
		{"success", 200, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`, ""},
		{"success null error", 200, `{"access_token":"new-token","error":null,"expires_in":3600}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("grant_type") != "refresh_token" {
					t.Error("missing refresh form")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			now := time.Unix(1700000000, 0)
			s := NewSource("")
			s.tokenURL = server.URL
			s.httpClient = server.Client()
			s.now = func() time.Time { return now }
			old := Credentials{AccessToken: "old-token", RefreshToken: "old-refresh", AccountID: "account"}
			got, err := s.refresh(context.Background(), old)
			if tc.reason != "" {
				expected := fmt.Sprintf("codex token refresh failed: status %d reason %s", tc.status, tc.reason)
				if err == nil || err.Error() != expected {
					t.Fatalf("error = %v, want %s", err, expected)
				}
				if strings.Contains(err.Error(), "private-account-secret") {
					t.Fatal("provider detail leaked")
				}
				if got != (Credentials{}) {
					t.Fatal("failed refresh returned credentials")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.AccessToken != "new-token" || got.AccountID != old.AccountID || !got.Expiry.Equal(now.Add(time.Hour)) {
				t.Fatal("refresh did not preserve identity and update expiry")
			}
			wantRefresh := "old-refresh"
			if tc.name == "success" {
				wantRefresh = "new-refresh"
			}
			if got.RefreshToken != wantRefresh {
				t.Fatal("refresh token rotation mismatch")
			}
		})
	}
}
