// SPDX-License-Identifier: MIT
package gateway

import (
	"net"
	"testing"
	"time"
)

func TestClientUsesConfiguredGatewayAddress(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("FXP_FIX_GATEWAY_ADDR", listener.Addr().String())
	accepted := make(chan struct{})
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()
	client := NewClient("fixture-user", "fixture-secret", nil)
	// This peer closes before LogonAck; the assertion concerns destination choice.
	if e = client.Connect(); e == nil {
		t.Fatal("expected closed peer error")
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("did not dial configured gateway")
	}
}
