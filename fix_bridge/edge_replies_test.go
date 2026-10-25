// SPDX-License-Identifier: MIT
package main

import (
	"fix_bridge/codec"
	"fix_bridge/session"
	"go_gateway/protobuf"
	"net"
	"reflect"
	"testing"
	"time"
)

func edgeSession(t *testing.T) (*bridgeHandler, *session.Session, func(string, ...codec.Field), func() *codec.Message) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { _ = cli.Close() })
	loggedOn := make(chan struct{})
	h := &seqTestHandler{bridgeHandler: &bridgeHandler{bridgeCompID: "B"}, onLogon: func() { close(loggedOn) }}
	s := session.NewSession(srv, "B", h)
	s.Auth = session.NewCredentialAuth(map[string]string{"T": "test-password"})
	h.sess = s
	go s.Run()
	t.Cleanup(s.Close)
	parser := codec.NewParser(cli)
	seq := 0
	send := func(typ string, fields ...codec.Field) {
		seq++
		b := codec.NewBuilder(typ, "T", "B", seq)
		for _, f := range fields {
			b.Set(f.Tag, f.Value)
		}
		go func() { _, _ = cli.Write(b.Build()) }()
	}
	next := func() *codec.Message {
		t.Helper()
		_ = cli.SetReadDeadline(time.Now().Add(time.Second))
		m, err := parser.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	send("A", codec.Field{Tag: 98, Value: "0"}, codec.Field{Tag: 108, Value: "30"}, codec.Field{Tag: 554, Value: "test-password"})
	next()
	<-loggedOn
	return h.bridgeHandler, s, send, next
}
func TestRejectedSingleStatusIsPrivateExecutionReport(t *testing.T) {
	h, _, _, next := edgeSession(t)
	var shapes []map[int]string
	for _, order := range []string{"missing", "foreign"} {
		m := &codec.Message{Fields: map[int]string{35: "H", 34: "2", 11: order, 55: "MSFT", 54: "1", 790: "request"}}
		q := &protobuf.OrderStatusRequest{Sender: "T", RequestId: "request", OrderId: order}
		h.trackStatus(m, q)
		h.onStatus(&protobuf.OrderStatusResponse{RequestId: q.RequestId, Sender: "T", RejectReason: "Unknown order", Complete: true})
		reply := next()
		for tag, want := range map[int]string{35: "8", 150: "I", 39: "8", 103: "5", 17: "0", 37: "NONE", 11: order, 790: "request", 58: "Unknown order", 14: "0", 151: "0", 6: "0"} {
			if reply.Get(tag) != want {
				t.Fatalf("tag %d=%q want %q", tag, reply.Get(tag), want)
			}
		}
		shape := map[int]string{}
		for tag, value := range reply.Fields {
			switch tag {
			case 9, 10, 11, 34, 52:
				continue
			}
			shape[tag] = value
		}
		shapes = append(shapes, shape)
	}
	if !reflect.DeepEqual(shapes[0], shapes[1]) {
		t.Fatal("unknown and foreign status disclose different information")
	}
}
func TestUnsupportedTypesPrecedeBodyDictionaryValidation(t *testing.T) {
	_, _, send, next := edgeSession(t)
	for _, typ := range []string{"AE", "AD", "ZZ"} {
		send(typ)
		m := next()
		if m.MsgType() != "j" || m.Get(380) != "3" || m.Get(372) != typ {
			t.Fatalf("unsupported %s reply: %v", typ, m.Fields)
		}
	}
	send("D")
	m := next()
	if m.MsgType() != "3" || m.Get(373) != "1" {
		t.Fatal("supported type bypassed dictionary")
	}
}
