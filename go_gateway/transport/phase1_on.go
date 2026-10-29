//go:build phase1

// SPDX-License-Identifier: MIT
package transport

import "sync/atomic"

const phase1Enabled = true

var phase1Encodes, phase1Locks, phase1Writes atomic.Uint64

func phase1Incremental(raw []byte) bool { return len(raw) > 0 && raw[0] == 0x52 }
func phase1Count(kind string) {
	switch kind {
	case "encodes":
		phase1Encodes.Add(1)
	case "locks":
		phase1Locks.Add(1)
	case "writes":
		phase1Writes.Add(1)
	}
}
