package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// Explicit lifetime guarantees require an unambiguous managed access-token
// JWT. Persisted expiry can shorten its lifetime, never extend it. Ordinary
// Get retains compatibility with other existing credential formats.
func knownAuthExpiry(data []byte) (time.Time, bool) {
	file, ok := uniqueAuthObject(data)
	if !ok {
		return time.Time{}, false
	}
	tokens, ok := uniqueAuthObject(file["tokens"])
	if !ok {
		return time.Time{}, false
	}
	var access, account string
	if json.Unmarshal(tokens["access_token"], &access) != nil || json.Unmarshal(tokens["account_id"], &account) != nil || strings.TrimSpace(account) == "" {
		return time.Time{}, false
	}
	var persisted time.Time
	if raw, present := tokens["expires_at"]; present {
		var exp int64
		if json.Unmarshal(raw, &exp) != nil || exp <= 0 {
			return time.Time{}, false
		}
		persisted = time.Unix(exp, 0)
	}
	return knownTokenExpiry(access, persisted)
}

func knownTokenExpiry(access string, persisted time.Time) (time.Time, bool) {
	parts := strings.Split(access, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	claims, ok := uniqueAuthObject(payload)
	if !ok {
		return time.Time{}, false
	}
	var exp int64
	if json.Unmarshal(claims["exp"], &exp) != nil || exp <= 0 {
		return time.Time{}, false
	}
	expires := time.Unix(exp, 0)
	if expires.Year() > 9999 {
		return time.Time{}, false
	}
	if !persisted.IsZero() && persisted.Before(expires) {
		expires = persisted
	}
	return expires, true
}

func uniqueAuthObject(data []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, false
	}
	return fields, true
}
