package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secretToken = "secret-access-token"

func TestParseValidAuth(t *testing.T) {
	creds, err := Parse([]byte(`{"tokens":{"access_token":"` + secretToken + `","account_id":"acct_123"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if creds.AccessToken != secretToken {
		t.Fatalf("AccessToken = %q", creds.AccessToken)
	}
	if creds.AccountID != "acct_123" {
		t.Fatalf("AccountID = %q", creds.AccountID)
	}
}

func TestParseOAuthFieldsAndJWTExpiry(t *testing.T) {
	expiry := time.Date(2026, 7, 22, 6, 33, 34, 0, time.UTC)
	token := testJWT(expiry)
	creds, err := Parse([]byte(fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"refresh-secret","id_token":"id-secret","account_id":"acct_123"},"last_refresh":"2026-07-12T06:33:34Z"}`, token)))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !creds.Expiry.Equal(expiry) || creds.RefreshToken != "refresh-secret" || creds.IDToken != "id-secret" {
		t.Fatalf("Parse() = %#v", creds)
	}
	if want := time.Date(2026, 7, 12, 6, 33, 34, 0, time.UTC); !creds.LastRefresh.Equal(want) {
		t.Fatalf("LastRefresh = %s, want %s", creds.LastRefresh, want)
	}
}

func TestParseOpaqueAccessTokenHasUnknownExpiry(t *testing.T) {
	creds, err := Parse([]byte(`{"tokens":{"access_token":"opaque-token","account_id":"acct_123"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !creds.Expiry.IsZero() {
		t.Fatalf("Expiry = %s, want zero", creds.Expiry)
	}
}

func testJWT(expiry time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())))
	return header + "." + payload + ".signature"
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw-user@example.test-secret-access-token-auth.json")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing file error")
	}
	if !errors.Is(err, ErrCredentialUnreadable) {
		t.Fatalf("Load() error = %v, want ErrCredentialUnreadable", err)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "raw-user@example.test") {
		t.Fatalf("Load() error leaked credential path or identity: %v", err)
	}
	assertNoSecret(t, err)
}

func TestParseMalformedAuth(t *testing.T) {
	_, err := Parse([]byte(`{"tokens":{"access_token":"` + secretToken + `"`))
	if err == nil {
		t.Fatal("Parse() error = nil, want malformed JSON error")
	}
	if !errors.Is(err, ErrCredentialInvalidJSON) {
		t.Fatalf("Parse() error = %v, want ErrCredentialInvalidJSON", err)
	}
	assertNoSecret(t, err)
}

func TestParseMissingTokens(t *testing.T) {
	_, err := Parse([]byte(`{"other":"value"}`))
	if err == nil {
		t.Fatal("Parse() error = nil, want missing tokens error")
	}
	if !errors.Is(err, ErrCredentialSchema) {
		t.Fatalf("Parse() error = %v, want ErrCredentialSchema", err)
	}
	assertNoSecret(t, err)
}

func TestParseMissingAccessToken(t *testing.T) {
	_, err := Parse([]byte(`{"tokens":{"account_id":"acct_123"}}`))
	if err == nil {
		t.Fatal("Parse() error = nil, want missing access token error")
	}
	assertNoSecret(t, err)
}

func TestParseMissingAccountID(t *testing.T) {
	_, err := Parse([]byte(`{"tokens":{"access_token":"` + secretToken + `"}}`))
	if err == nil {
		t.Fatal("Parse() error = nil, want missing account ID error")
	}
	assertNoSecret(t, err)
}

func TestLoadValidAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"`+secretToken+`","account_id":"acct_123"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	creds, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if creds.AccessToken != secretToken || creds.AccountID != "acct_123" {
		t.Fatalf("Load() = %#v", creds)
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), secretToken) {
		t.Fatalf("error leaked secret token: %v", err)
	}
}
