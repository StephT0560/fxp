// SPDX-License-Identifier: MIT
package coreauth

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

func TestTokenHandshakeAndRedactedFailure(t *testing.T) {
	for _, token := range []string{"correct-secret-at-least-24-bytes", "wrong-secret-at-least-24-bytes"} {
		client, server := net.Pipe()
		done := make(chan struct{})
		authenticated := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			var header [10]byte
			if _, err := io.ReadFull(server, header[:]); err != nil {
				return
			}
			if string(header[:8]) != "FXPAUTH1" {
				return
			}
			data := make([]byte, binary.BigEndian.Uint16(header[8:]))
			if _, err := io.ReadFull(server, data); err != nil {
				return
			}
			if string(data) == "correct-secret-at-least-24-bytes" {
				_, _ = server.Write([]byte{1})
				// A successful pool handshake keeps the peer alive. Do not race
				// its Close against the client clearing its read deadline.
				<-authenticated
			}
		}()
		err := Authenticate(client, token)
		close(authenticated)
		_ = client.Close()
		<-done
		if (err == nil) != (token == "correct-secret-at-least-24-bytes") {
			t.Fatalf("handshake result: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Fatal("token leaked")
		}
	}
}
func TestDisabledHandshakeDoesNotConsumeOrWriteProtocolBytes(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	if err := Authenticate(client, ""); err != nil {
		t.Fatal(err)
	}
}
