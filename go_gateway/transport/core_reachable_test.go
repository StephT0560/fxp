// SPDX-License-Identifier: MIT

package transport

import (
	"net"
	"testing"
)

func TestCoreReachableFollowsMarketDataConnection(t *testing.T) {
	saved := mdConn
	t.Cleanup(func() { mdConn = saved })

	mdConn = nil
	if !coreReachable() {
		t.Fatal("no pool yet must not gate connections")
	}
	mdConn = &pooledConn{}
	if coreReachable() {
		t.Fatal("down market-data connection must report core unreachable")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	mdConn.conn = a
	if !coreReachable() {
		t.Fatal("established market-data connection must report core reachable")
	}
}

func TestDisconnectClientsClosesEveryClient(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	clientWriters.Store(a, &clientWriter{})
	t.Cleanup(func() { clientWriters.Delete(a) })
	disconnectClients()
	if _, err := a.Write([]byte("x")); err == nil {
		t.Fatal("client connection still open after disconnectClients")
	}
}

func TestCoreLossDisconnectSkipsCancelOnDisconnect(t *testing.T) {
	saved := cancelOnDisconnect
	t.Cleanup(func() { cancelOnDisconnect = saved })
	var canceled []string
	cancelOnDisconnect = func(trader string) { canceled = append(canceled, trader) }

	start := func(sid string) {
		sessionStore.Store(sid, "demo:alice")
		sessionCOD.Store(sid, struct{}{})
	}
	a, b := net.Pipe()
	defer b.Close()
	clientWriters.Store(a, &clientWriter{})
	t.Cleanup(func() { clientWriters.Delete(a) })

	start("core-loss")
	disconnectClients()
	endSessionFor(a, "core-loss")
	if len(canceled) != 0 {
		t.Fatalf("gateway-initiated disconnect canceled orders for %v", canceled)
	}

	start("client-drop")
	endSessionFor(a, "client-drop")
	if len(canceled) != 1 || canceled[0] != "demo:alice" {
		t.Fatalf("client disconnect must still cancel on disconnect, got %v", canceled)
	}
}
