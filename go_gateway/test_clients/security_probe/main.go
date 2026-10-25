// SPDX-License-Identifier: MIT
// Functional TLS/mTLS/core-boundary probe, not a performance measurement.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	"go_gateway/coreauth"
	"go_gateway/protobuf"
	"go_gateway/test_clients/devjwt"
	"go_gateway/tlsconfig"
)

func frame(m *protobuf.FXPMessage) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	out := make([]byte, 4, len(b)+4)
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	return append(out, b...)
}
func read(c net.Conn) (*protobuf.FXPMessage, error) {
	var p [4]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(p[:])
	if n == 0 || n > 65536 {
		return nil, errors.New("bad response size")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return nil, err
	}
	m := new(protobuf.FXPMessage)
	return m, proto.Unmarshal(b, m)
}
func logon(id string) *protobuf.FXPMessage {
	return &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: devjwt.Generate(os.Getenv("FXP_JWT_SECRET"), id), SessionId: fmt.Sprintf("%s-%d", id, time.Now().UnixNano()), EncryptionType: protobuf.EncryptionType_TLS_1_3}}}
}
func run() error {
	options, err := tlsconfig.ClientFromEnv("FXP_CLIENT_")
	if err != nil {
		return err
	}
	if !options.Enabled {
		return errors.New("security probe requires TLS")
	}
	c, err := options.Dial(context.Background(), "127.0.0.1:8082", 3*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = c.Write(frame(logon("TLS-TCP"))); err != nil {
		return err
	}
	ack, err := read(c)
	if err != nil || ack.GetLogonAck() == nil || !ack.GetLogonAck().Accepted {
		return errors.New("TLS TCP Logon failed")
	}
	fmt.Println("PASS verified TLS/mTLS gateway TCP Logon")
	cfg, err := options.Config()
	if err != nil {
		return err
	}
	d := websocket.Dialer{TLSClientConfig: cfg, HandshakeTimeout: 3 * time.Second}
	headers := http.Header{"Origin": []string{"https://127.0.0.1:8081"}}
	ws, _, err := d.Dial("wss://127.0.0.1:8081/ws", headers)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()
	b, err := proto.Marshal(logon("TLS-WS"))
	if err != nil {
		return err
	}
	if err = ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, b, err = ws.ReadMessage()
	if err != nil {
		return err
	}
	m := new(protobuf.FXPMessage)
	if err = proto.Unmarshal(b, m); err != nil || m.GetLogonAck() == nil || !m.GetLogonAck().Accepted {
		return errors.New("WSS Logon failed")
	}
	fmt.Println("PASS WSS Logon and HTTPS same-origin handshake")
	bad := cfg.Clone()
	bad.RootCAs = x509.NewCertPool()
	if wrong, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", "127.0.0.1:8082", bad); err == nil {
		_ = wrong.Close()
		return errors.New("gateway accepted wrong CA")
	}
	fmt.Println("PASS wrong server CA rejected")
	noCert := cfg.Clone()
	noCert.Certificates = nil
	noCert.GetClientCertificate = nil
	plainTLS, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", "127.0.0.1:8082", noCert)
	if err == nil {
		_ = plainTLS.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = plainTLS.Write(frame(logon("TLS-NOCERT")))
		_, err = read(plainTLS)
		_ = plainTLS.Close()
	}
	if err == nil {
		return errors.New("gateway accepted missing client certificate")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		return errors.New("missing-client-cert probe stalled instead of rejecting")
	}
	fmt.Println("PASS required client certificate enforced")
	// A native frame must not reach sender dispatch before core authentication.
	direct, err := net.DialTimeout("tcp", "127.0.0.1:8080", time.Second)
	if err != nil {
		return err
	}
	_ = direct.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = direct.Write(frame(&protobuf.FXPMessage{Payload: &protobuf.FXPMessage_NewOrder{NewOrder: &protobuf.NewOrderSingle{OrderId: "forged", Sender: "forged-owner", Symbol: "UNTRUSTED", Price: 100, Quantity: 1, Side: protobuf.OrderSide_BUY, OrderType: protobuf.OrderType_LIMIT, TimeInForce: protobuf.TimeInForce_GTC}}}))
	_, err = read(direct)
	_ = direct.Close()
	if err == nil {
		return errors.New("unauthenticated core order accepted")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		return errors.New("unauthenticated core order was not rejected promptly")
	}
	fmt.Println("PASS unauthenticated core sender rejected")
	direct, err = net.DialTimeout("tcp", "127.0.0.1:8080", time.Second)
	if err != nil {
		return err
	}
	err = coreauth.Authenticate(direct, "deliberately-wrong-token")
	_ = direct.Close()
	if err == nil {
		return errors.New("wrong core token accepted")
	}
	fmt.Println("PASS wrong core token rejected")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "security probe failed:", err)
		os.Exit(1)
	}
}
