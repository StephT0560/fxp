// SPDX-License-Identifier: MIT
// Package symbolconfig shares the venue's integer-minor-unit price definition.
package symbolconfig

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

type Rules struct {
	PriceScale                        int
	TickSize                          int64
	LotSize, MinQuantity, MaxQuantity uint32
}
type Config map[string]Rules

var current atomic.Pointer[Config]

func Default() Rules { return Rules{2, 1, 1, 1, 2147483647} }
func (c Config) For(symbol string) Rules {
	if r, ok := c[symbol]; ok {
		return r
	}
	return Default()
}
func Current() Config {
	if c := current.Load(); c != nil {
		return *c
	}
	return nil
}

// Set installs an immutable configuration before serving requests.
func Set(c Config) {
	copyConfig := make(Config, len(c))
	for s, r := range c {
		copyConfig[s] = r
	}
	current.Store(&copyConfig)
}
func Configure(path string) error {
	c := Config{}
	if path != "" {
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var e2 error
		c, e2 = Parse(string(data))
		if e2 != nil {
			return e2
		}
	}
	Set(c)
	return nil
}
func Parse(text string) (Config, error) {
	c := Config{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || line == "symbol,price_scale,tick_size,lot_size,min_quantity,max_quantity" {
			continue
		}
		cols := strings.Split(line, ",")
		bad := func() (Config, error) { return nil, fmt.Errorf("invalid symbol configuration at line %d", i+1) }
		if len(cols) != 6 {
			return bad()
		}
		for j := range cols {
			cols[j] = strings.TrimSpace(cols[j])
		}
		if cols[0] == "" {
			return bad()
		}
		for _, v := range []byte(cols[0]) {
			switch {
			case v >= 'A' && v <= 'Z', v >= 'a' && v <= 'z', v >= '0' && v <= '9', strings.ContainsRune("._-/:", rune(v)):
			default:
				return bad()
			}
		}
		nums := make([]uint64, 5)
		for j := range nums {
			for _, v := range []byte(cols[j+1]) {
				if v < '0' || v > '9' {
					return bad()
				}
			}
			n, e := strconv.ParseUint(cols[j+1], 10, 64)
			if e != nil {
				return bad()
			}
			nums[j] = n
		}
		scale, tick, lot, minq, maxq := nums[0], nums[1], nums[2], nums[3], nums[4]
		if scale > 8 || tick == 0 || tick > uint64(1<<63-1) || lot == 0 || lot > 2147483647 || minq == 0 || minq > maxq || maxq > 2147483647 || ((minq+lot-1)/lot)*lot > maxq {
			return bad()
		}
		if _, ok := c[cols[0]]; ok {
			return nil, fmt.Errorf("duplicate symbol at line %d", i+1)
		}
		c[cols[0]] = Rules{int(scale), int64(tick), uint32(lot), uint32(minq), uint32(maxq)}
	}
	return c, nil
}
func (c Config) ParsePrice(symbol, text string) (int64, error) {
	r := c.For(symbol)
	_, configured := c[symbol]
	return parsePrice(text, r.PriceScale, !configured)
}

// ParseDefault preserves historical two-decimal half-away-from-zero rounding.
func ParseDefault(text string) (int64, error) { return parsePrice(text, 2, true) }
func parsePrice(text string, scale int, round bool) (int64, error) {
	text = strings.TrimSpace(text)
	original := text
	neg := strings.HasPrefix(text, "-")
	if neg {
		text = text[1:]
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || len(parts) == 0 || parts[0] == "" {
		return 0, fmt.Errorf("invalid price %q", original)
	}
	for _, p := range parts {
		if p == "" {
			return 0, fmt.Errorf("invalid price %q", original)
		}
		for _, v := range []byte(p) {
			if v < '0' || v > '9' {
				return 0, fmt.Errorf("invalid price %q", original)
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	increase := false
	if len(fraction) > scale {
		extra := fraction[scale:]
		if !round && strings.Trim(extra, "0") != "" {
			return 0, fmt.Errorf("price exceeds symbol precision")
		}
		increase = round && extra[0] >= '5'
		fraction = fraction[:scale]
	}
	fraction += strings.Repeat("0", scale-len(fraction))
	whole := strings.TrimLeft(parts[0], "0")
	if whole == "" {
		whole = "0"
	}
	if len(whole) > 19 {
		return 0, fmt.Errorf("price out of int64 range")
	}
	n, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok {
		return 0, fmt.Errorf("invalid price %q", original)
	}
	if increase {
		n.Add(n, big.NewInt(1))
	}
	if neg {
		n.Neg(n)
	}
	if !n.IsInt64() {
		return 0, fmt.Errorf("price out of int64 range")
	}
	return n.Int64(), nil
}
func (c Config) FormatPrice(symbol string, value int64) string {
	return FormatScale(value, c.For(symbol).PriceScale)
}
func FormatScale(value int64, scale int) string {
	s := strconv.FormatInt(value, 10)
	sign := ""
	if s[0] == '-' {
		sign = "-"
		s = s[1:]
	}
	if scale == 0 {
		return sign + s
	}
	if len(s) <= scale {
		s = strings.Repeat("0", scale+1-len(s)) + s
	}
	return sign + s[:len(s)-scale] + "." + s[len(s)-scale:]
}
