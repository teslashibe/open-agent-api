package auth

import (
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

func Load(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}
	return creds, nil
}

func Parse(data []byte) (Credentials, error) {
	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Credentials{}, fmt.Errorf("invalid auth JSON: %w", err)
	}
	if file.Tokens == nil {
		return Credentials{}, errors.New("missing tokens")
	}
	if file.Tokens.AccessToken == "" {
		return Credentials{}, errors.New("missing tokens.access_token")
	}
	if file.Tokens.AccountID == "" {
		return Credentials{}, errors.New("missing tokens.account_id")
	}
	return Credentials{
		AccessToken: file.Tokens.AccessToken,
		AccountID:   file.Tokens.AccountID,
	}, nil
}
