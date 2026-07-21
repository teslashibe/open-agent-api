package server

import (
	"crypto/subtle"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// bearerAuthMiddleware rejects requests that do not carry the shared gateway
// secret. An empty secret disables the gate entirely so local Cursor-style
// clients can keep sending arbitrary Authorization values.
func bearerAuthMiddleware(secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if secret == "" {
			return c.Next()
		}
		token, ok := bearerToken(c.Get(fiber.HeaderAuthorization))
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
			return writeError(c, fiber.StatusUnauthorized, "authentication_error", "authentication failed")
		}
		return c.Next()
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return header[len(prefix):], true
}
