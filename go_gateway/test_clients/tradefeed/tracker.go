// SPDX-License-Identifier: MIT

package tradefeed

type Tracker struct {
	last map[string]uint64
}

func (t *Tracker) Observe(symbol string, seq uint64) (gap bool, expected uint64) {
	if t.last == nil {
		t.last = make(map[string]uint64)
	}
	expected = t.last[symbol] + 1
	t.last[symbol] = seq
	return seq != expected, expected
}
