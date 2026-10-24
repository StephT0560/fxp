// SPDX-License-Identifier: MIT

//go:build unix

package transport

import (
	"net"
	"syscall"
	"testing"
)

func TestDialCoreSetsNoDelay(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer func() { _ = c.Close() }()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}()
	c, err := dialCore(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		v, serr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
	}); err != nil || serr != nil {
		t.Fatal(err, serr)
	}
	if v == 0 {
		t.Fatal("TCP_NODELAY is off")
	}
}
