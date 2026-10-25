// SPDX-License-Identifier: MIT
package main

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func (a *app) proxy(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		http.Error(w, "same-origin WebSocket required", http.StatusForbidden)
		return
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	// Deliberately construct no upstream headers. In particular, browser Origin,
	// cookies, Authorization and forwarded-host headers never reach the gateway.
	upstream, response, err := dialer.DialContext(r.Context(), a.cfg.upstream, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		http.Error(w, "gateway unavailable", http.StatusBadGateway)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: a.sameOrigin, HandshakeTimeout: 5 * time.Second}
	client, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = upstream.Close()
		return
	}
	upstream.SetReadLimit(maxWSMessage)
	client.SetReadLimit(maxWSMessage)
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	a.sockets[client] = struct{}{}
	a.sockets[upstream] = struct{}{}
	a.mu.Unlock()
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
		a.mu.Lock()
		delete(a.sockets, client)
		delete(a.sockets, upstream)
		a.mu.Unlock()
	}()
	errors := make(chan error, 2)
	copyFrames := func(dst, src *websocket.Conn) {
		for {
			kind, data, err := src.ReadMessage()
			if err != nil {
				errors <- err
				return
			}
			if kind != websocket.BinaryMessage {
				errors <- &websocket.CloseError{Code: websocket.CloseUnsupportedData, Text: "binary frames required"}
				return
			}
			_ = dst.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := dst.WriteMessage(kind, data); err != nil {
				errors <- err
				return
			}
		}
	}
	go copyFrames(upstream, client)
	go copyFrames(client, upstream)
	err = <-errors
	code, reason := websocket.CloseGoingAway, "connection closed"
	if closeErr, ok := err.(*websocket.CloseError); ok {
		code, reason = closeErr.Code, closeErr.Text
	}
	if err == websocket.ErrReadLimit {
		code, reason = websocket.CloseMessageTooBig, "message too large"
	}
	if code == websocket.CloseAbnormalClosure || code == websocket.CloseNoStatusReceived || code == websocket.CloseTLSHandshake {
		code = websocket.CloseGoingAway
	}
	for _, conn := range []*websocket.Conn{client, upstream} {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	<-errors // Both copying goroutines must exit before the handler returns.
}
