//go:build !phase1

// SPDX-License-Identifier: MIT
package transport

// Compile-time false removes every instrumentation branch from normal builds.
const phase1Enabled = false

func phase1Count(string)            {}
func phase1Incremental([]byte) bool { return false }
