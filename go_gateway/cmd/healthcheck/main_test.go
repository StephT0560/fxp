// SPDX-License-Identifier: MIT
package main

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

func TestProbeListenerAndCoreAuthentication(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			var header [10]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return
			}
			size := binary.BigEndian.Uint16(header[8:])
			if string(header[:8]) != "FXPAUTH1" || size != 32 {
				return
			}
			token := make([]byte, size)
			if _, err := io.ReadFull(conn, token); err != nil {
				return
			}
			if accepted {
				_, _ = conn.Write([]byte{1})
			} else {
				_, _ = conn.Write([]byte{0})
			}
		}()
		err = probe(listener.Addr().String(), strings.Repeat("x", 32), true)
		if (err == nil) != accepted {
			t.Fatalf("accepted=%v, err=%v", accepted, err)
		}
		if err != nil && strings.Contains(err.Error(), strings.Repeat("x", 32)) {
			t.Fatal("secret leaked")
		}
		listener.Close()
		<-done
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := probe(address, "", false); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if err := probe(address, "", false); err == nil {
		t.Fatal("closed listener accepted")
	}
}
