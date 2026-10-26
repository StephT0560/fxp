// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"go_gateway/protobuf"
	"go_gateway/test_clients/devjwt"
	"go_gateway/tlsconfig"

	"google.golang.org/protobuf/proto"
)

const operatorSubject = "demo-operator"

// openCall asks the gateway, under the BFF's operator identity, to put every
// symbol into an opening call that uncrosses after seconds.
func (c config) openCall(ctx context.Context, seconds int) error {
	options, err := tlsconfig.ClientFromEnv("FXP_CLIENT_")
	if err != nil {
		return errors.New("invalid client TLS settings")
	}
	conn, err := options.Dial(ctx, c.simGateway, 5*time.Second)
	if err != nil {
		return errors.New("gateway unreachable")
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	token, err := devjwt.GenerateOperator(c.secret, operatorSubject, time.Minute)
	if err != nil {
		return err
	}
	if err := writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_Logon{Logon: &protobuf.Logon{Token: token}}}); err != nil {
		return errors.New("gateway logon failed")
	}
	ack, err := readFrame(conn)
	if err != nil || ack.GetLogonAck() == nil {
		return errors.New("gateway logon failed")
	}
	if !ack.GetLogonAck().Accepted {
		return fmt.Errorf("gateway logon rejected: %s", ack.GetLogonAck().RejectReason)
	}
	id := "open-call-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	control := &protobuf.SessionControl{RequestId: id, OpeningCallSeconds: uint32(seconds)}
	if err := writeFrame(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_SessionControl{SessionControl: control}}); err != nil {
		return errors.New("session control not sent")
	}
	for {
		msg, err := readFrame(conn)
		if err != nil {
			return errors.New("no session control report")
		}
		report := msg.GetSessionControlReport()
		if report == nil || report.RequestId != id {
			continue
		}
		if report.RejectReason != "" {
			return fmt.Errorf("rejected: %s", report.RejectReason)
		}
		if report.SymbolsAffected == 0 {
			return errors.New("core has no symbols")
		}
		return nil
	}
}

func writeFrame(conn net.Conn, msg *protobuf.FXPMessage) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	_, err = conn.Write(frame)
	return err
}

func readFrame(conn net.Conn) (*protobuf.FXPMessage, error) {
	var h [4]byte
	if _, err := io.ReadFull(conn, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > 1<<20 {
		return nil, errors.New("invalid frame length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	msg := &protobuf.FXPMessage{}
	return msg, proto.Unmarshal(body, msg)
}
