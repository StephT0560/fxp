// SPDX-License-Identifier: MIT
package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"go_gateway/startup"
)

type config struct {
	addr, origin, upstream, staticDir, simBinary, simGateway, secret, symbolFile string
	tokenTTL                                                                     time.Duration
	tokenRate                                                                    int
	allowPlaintext                                                               bool
}

func defaultConfig() config {
	return config{
		addr: "127.0.0.1:3000", origin: "http://127.0.0.1:3000", upstream: "ws://127.0.0.1:8081/ws",
		staticDir: "web/dist", simBinary: "bin/simday", simGateway: "127.0.0.1:8082", tokenTTL: 15 * time.Minute, tokenRate: 10,
	}
}
func configFromEnv() (config, error) {
	c := defaultConfig()
	for name, dst := range map[string]*string{
		"FXP_DEMO_ADDR": &c.addr, "FXP_DEMO_ORIGIN": &c.origin, "FXP_DEMO_GATEWAY_WS_URL": &c.upstream,
		"FXP_DEMO_STATIC_DIR": &c.staticDir, "FXP_DEMO_SIM_BINARY": &c.simBinary, "FXP_SIM_GATEWAY_ADDR": &c.simGateway,
	} {
		if v := os.Getenv(name); v != "" {
			*dst = v
		}
	}
	c.secret = os.Getenv("FXP_JWT_SECRET")
	c.symbolFile = os.Getenv("FXP_SYMBOL_CONFIG")
	for name, dst := range map[string]*int{"FXP_DEMO_TOKEN_RATE_PER_MIN": &c.tokenRate} {
		if v := os.Getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			*dst = n
		}
	}
	if v := os.Getenv("FXP_DEMO_TOKEN_TTL_SEC"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 900 {
			return c, fmt.Errorf("FXP_DEMO_TOKEN_TTL_SEC must be 1..900")
		}
		c.tokenTTL = time.Duration(n) * time.Second
	}
	if v := os.Getenv("FXP_ALLOW_PLAINTEXT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("FXP_ALLOW_PLAINTEXT must be boolean")
		}
		c.allowPlaintext = b
	}
	return c, c.validate()
}
func (c config) validate() error {
	if len(c.secret) < startup.MinSecretBytes || c.secret == startup.DevJWTSecret {
		return fmt.Errorf("FXP_JWT_SECRET must be non-default and contain at least %d bytes", startup.MinSecretBytes)
	}
	host, _, err := net.SplitHostPort(c.addr)
	if err != nil {
		return fmt.Errorf("invalid FXP_DEMO_ADDR")
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && !c.allowPlaintext {
		return fmt.Errorf("non-loopback FXP_DEMO_ADDR requires FXP_ALLOW_PLAINTEXT=true")
	}
	u, err := url.Parse(c.origin)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("FXP_DEMO_ORIGIN must be an http origin without path")
	}
	u, err = url.Parse(c.upstream)
	if err != nil || u.Scheme != "ws" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("FXP_DEMO_GATEWAY_WS_URL must be a ws URL without credentials or query")
	}
	if c.tokenTTL < time.Second || c.tokenTTL > 15*time.Minute {
		return fmt.Errorf("token lifetime must be 1..900 seconds")
	}
	if c.tokenRate < 1 || c.tokenRate > 120 {
		return fmt.Errorf("FXP_DEMO_TOKEN_RATE_PER_MIN must be 1..120")
	}
	return nil
}
