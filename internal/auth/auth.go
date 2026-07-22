package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Credentials struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	Expiry       time.Time
	LastRefresh  time.Time
}

type authFile struct {
	Tokens      *tokens `json:"tokens"`
	LastRefresh string  `json:"last_refresh,omitempty"`
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	AccountID    string `json:"account_id"`
}

var (
	ErrCredentialUnreadable  = errors.New("credential file unreadable")
	ErrCredentialInvalidJSON = errors.New("credential file contains invalid JSON")
	ErrCredentialSchema      = errors.New("credential file schema invalid")
)

func Load(path string) (Credentials, error) {
	creds, _, err := LoadWithRevision(path)
	return creds, err
}

// LoadWithRevision validates a Codex credential file and returns a stable
// content revision used to detect an operator-provided credential reload. Its
// errors intentionally omit the path, file contents, tokens, and identities.
func LoadWithRevision(path string) (Credentials, [sha256.Size]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, [sha256.Size]byte{}, ErrCredentialUnreadable
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, [sha256.Size]byte{}, err
	}
	return creds, sha256.Sum256(data), nil
}

func Parse(data []byte) (Credentials, error) {
	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Credentials{}, ErrCredentialInvalidJSON
	}
	if file.Tokens == nil {
		return Credentials{}, fmt.Errorf("%w: missing tokens", ErrCredentialSchema)
	}
	if file.Tokens.AccessToken == "" {
		return Credentials{}, fmt.Errorf("%w: missing tokens.access_token", ErrCredentialSchema)
	}
	if file.Tokens.AccountID == "" {
		return Credentials{}, fmt.Errorf("%w: missing tokens.account_id", ErrCredentialSchema)
	}
	creds := Credentials{
		AccessToken:  file.Tokens.AccessToken,
		RefreshToken: file.Tokens.RefreshToken,
		IDToken:      file.Tokens.IDToken,
		AccountID:    file.Tokens.AccountID,
	}
	creds.Expiry = jwtExpiry(file.Tokens.AccessToken)
	if file.LastRefresh != "" {
		lastRefresh, err := time.Parse(time.RFC3339Nano, file.LastRefresh)
		if err != nil {
			return Credentials{}, fmt.Errorf("%w: invalid last_refresh", ErrCredentialSchema)
		}
		creds.LastRefresh = lastRefresh
	}
	return creds, nil
}

// jwtExpiry extracts exp without validating the JWT signature. Signature
// validation belongs to the issuer; locally we only need the expiry hint to
// avoid presenting a known-expired bearer token. Opaque legacy tokens remain
// supported and return a zero expiry so a 401/403 can drive one refresh.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil || claims.Exp == "" {
		return time.Time{}
	}
	exp, err := claims.Exp.Int64()
	if err != nil || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(exp, 0)
}
