package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Credentials struct {
	AccessToken string
	AccountID   string
}

type authFile struct {
	Tokens *tokens `json:"tokens"`
}

type tokens struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
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
	return Credentials{
		AccessToken: file.Tokens.AccessToken,
		AccountID:   file.Tokens.AccountID,
	}, nil
}
