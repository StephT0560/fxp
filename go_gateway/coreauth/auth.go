// SPDX-License-Identifier: MIT
// Package coreauth authenticates a trusted connection, not individual traders.
package coreauth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

var ErrAuthentication = errors.New("core authentication failed")

func Authenticate(conn net.Conn, token string) error {
	if token == "" {
		return nil
	}
	if len(token) > 1024 {
		return ErrAuthentication
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return ErrAuthentication
	}
	header := make([]byte, 10, 10+len(token))
	copy(header, "FXPAUTH1")
	binary.BigEndian.PutUint16(header[8:], uint16(len(token)))
	header = append(header, token...)
	if _, err := io.Copy(conn, bytes.NewReader(header)); err != nil {
		return ErrAuthentication
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil || ack[0] != 1 {
		return ErrAuthentication
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return ErrAuthentication
	}
	return nil
}
