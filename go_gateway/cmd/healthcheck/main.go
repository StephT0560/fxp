// SPDX-License-Identifier: MIT
// healthcheck supplies shell-free listener/core-auth readiness for release images.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"go_gateway/coreauth"
)

func probe(address, token string, core bool) error {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return fmt.Errorf("listener unavailable")
	}
	defer conn.Close()
	if core {
		if err := coreauth.Authenticate(conn, token); err != nil {
			return fmt.Errorf("core authentication failed")
		}
	}
	return nil
}

func main() {
	address := flag.String("addr", "127.0.0.1:8080", "local TCP listener")
	core := flag.Bool("core", false, "perform configured core authentication")
	flag.Parse()
	if err := probe(*address, os.Getenv("FXP_CORE_AUTH_TOKEN"), *core); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
