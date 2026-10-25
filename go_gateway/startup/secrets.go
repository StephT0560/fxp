// SPDX-License-Identifier: MIT
// Package startup validates deployment configuration before opening listeners.
package startup

import (
	"fmt"
	"go_gateway/coreauth"
	"net"
	"os"
)

const MinSecretBytes = 24
const DevJWTSecret = "dev-secret-change-in-production"

func ValidateSecrets(addresses ...string) error {
	external := false
	for _, addr := range addresses {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid listen address: %w", err)
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			external = true
		}
	}
	for _, name := range []string{"FXP_JWT_SECRET", "FXP_JWT_PREVIOUS_SECRET"} {
		secret := os.Getenv(name)
		if secret == "" && name == "FXP_JWT_PREVIOUS_SECRET" {
			continue
		}
		if len(secret) < MinSecretBytes {
			return fmt.Errorf("%s must contain at least 24 bytes", name)
		}
		if external && secret == DevJWTSecret {
			return fmt.Errorf("%s development default is forbidden on non-loopback listeners", name)
		}
	}
	return coreauth.ValidateToken(os.Getenv("FXP_CORE_AUTH_TOKEN"))
}
