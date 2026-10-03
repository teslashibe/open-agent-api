package auth

import (
	"context"
	"errors"
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
		{"invalid refresh token", 401, `{"error":{"code":"invalid_refresh_token","message":"private-account-secret"}}`, "invalid_refresh_token"},
		{"structured type", 401, `{"error":{"type":"invalid_grant","message":"private-account-secret"}}`, "invalid_grant"},
		{"unknown code", 400, `{"error":{"code":"private-account-secret"}}`, "unknown_error"},
		{"unknown string", 400, `{"error":"private-account-secret"}`, "unknown_error"},
		{"malformed", 502, `private-account-secret`, "invalid_response"},
		{"unexpected shape", 400, `{"error":["private-account-secret"]}`, "unknown_error"},
		{"number error", 400, `{"error":123}`, "unknown_error"},
		{"bool error", 400, `{"error":true}`, "unknown_error"},
		{"malformed code type", 401, `{"error":{"code":["private-account-secret"],"type":"invalid_grant"}}`, "unknown_error"},
		{"no provider error", 503, `{"error_description":"private-account-secret"}`, "unknown_error"},
		{"nested unknown code", 401, `{"error":{"code":{"token":"private-account-secret"}}}`, "unknown_error"},
		{"trailing response", 400, `{"error":"invalid_grant"} private-account-secret`, "invalid_response"},
		{"escaped unknown code", 400, `{"error":"invalid_grant\nprivate-account-secret"}`, "unknown_error"},
		{"oversized response", 400, `{"error":"invalid_grant","private":"` + strings.Repeat("x", tokenResponseMaxBytes) + `"}`, "oversized_response"},
		{"valid prefix past bound", 200, `{"access_token":"private-account-secret"}` + strings.Repeat(" ", tokenResponseMaxBytes) + `x`, "oversized_response"},
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

type refreshResponseTransport func(*http.Request) (*http.Response, error)

func (f refreshResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type refreshFailedBody struct{}

func (refreshFailedBody) Read([]byte) (int, error) {
	return 0, errors.New("private-body-token-account")
}
func (refreshFailedBody) Close() error { return nil }
func TestRefreshUnreadableBodyDoesNotLeakErrorChain(t *testing.T) {
	s := NewSource("")
	s.httpClient = &http.Client{Transport: refreshResponseTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 502, Body: refreshFailedBody{}, Header: http.Header{}}, nil
	})}
	got, err := s.refresh(context.Background(), Credentials{RefreshToken: "synthetic-refresh"})
	if got != (Credentials{}) || err == nil || err.Error() != "codex token refresh failed: status 502 reason unreadable_response" || errors.Unwrap(err) != nil {
		t.Fatal("read failure leaked raw transport details")
	}
}
