// SPDX-License-Identifier: MIT
package symbolconfig

import (
	"math"
	"testing"
)

func TestSymbolConfigScalesAndInt64Boundaries(t *testing.T) {
	c, err := Parse("P0,0,1,1,1,2147483647\nP2,2,5,10,10,1000\nP4,4,25,1,1,20\nP8,8,1,1,1,20\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		s, max, over string
		scale        int
	}{{"P0", "9223372036854775807", "9223372036854775808", 0}, {"P2", "92233720368547758.07", "92233720368547758.08", 2}, {"P4", "922337203685477.5807", "922337203685477.5808", 4}, {"P8", "92233720368.54775807", "92233720368.54775808", 8}} {
		if c.For(tc.s).PriceScale != tc.scale {
			t.Fatal(tc)
		}
		n, e := c.ParsePrice(tc.s, tc.max)
		if e != nil || n != math.MaxInt64 {
			t.Fatalf("%s: %d %v", tc.s, n, e)
		}
		if c.FormatPrice(tc.s, n) != tc.max {
			t.Fatal("format", tc.s)
		}
		if _, e = c.ParsePrice(tc.s, tc.over); e == nil {
			t.Fatal("overflow accepted", tc.s)
		}
	}
	if _, e := c.ParsePrice("P2", "1.005"); e == nil {
		t.Fatal("configured precision rounded")
	}
	if n, e := c.ParsePrice("unlisted", "1.005"); e != nil || n != 101 {
		t.Fatal("legacy rounding", n, e)
	}
	if c.FormatPrice("P8", math.MinInt64) != "-92233720368.54775808" {
		t.Fatal("negative format overflow")
	}
}
