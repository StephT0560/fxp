//go:build !linux

// SPDX-License-Identifier: MIT
package diagnostics

func monotonicNS() int64 { return 0 }
