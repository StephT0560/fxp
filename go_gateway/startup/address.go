// SPDX-License-Identifier: MIT
package startup

import "os"

// Address returns an explicit deployment address or the loopback default.
func Address(variable, fallback string) string {
	if value := os.Getenv(variable); value != "" {
		return value
	}
	return fallback
}
