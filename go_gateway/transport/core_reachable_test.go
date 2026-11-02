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
