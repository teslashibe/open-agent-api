package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type idGenerator func() string

func newUUIDV7String() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

func turnMetadata(installationID, sessionID, requestKind, turnID string) string {
	return fmt.Sprintf(
		`{"installation_id": %q, "session_id": %q, "thread_id": %q, "turn_id": %q, "window_id": %q, "request_kind": %q, "thread_source": "user", "sandbox": "seatbelt"}`,
		installationID,
		sessionID,
		sessionID,
		turnID,
		sessionID+":0",
		requestKind,
	)
}

func loadInstallationID(codexHome string, fallback idGenerator) string {
	data, err := os.ReadFile(filepath.Join(codexHome, "installation_id"))
	if err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	return fallback()
}
