// SPDX-License-Identifier: MIT
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go_gateway/protobuf"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type subscriberHandle interface {
	Finish() error
	Close() error
	Updates() int64
}
type subscribers struct {
	expected int64
	timeout  time.Duration
	conns    []net.Conn
	readers  sync.WaitGroup
	updates  atomic.Int64
	closing  atomic.Bool
	errors   chan error
	once     sync.Once
	err      error
}

func (s *subscribers) Updates() int64 { return s.updates.Load() }
func (s *subscribers) Finish() error {
	deadline := time.NewTimer(s.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for s.updates.Load() < s.expected {
		select {
		case err := <-s.errors:
			_ = s.Close()
			return err
		case <-deadline.C:
			_ = s.Close()
			return errors.New("incomplete market-data drain")
		case <-ticker.C:
		}
	}
	return s.Close()
}
func (s *subscribers) Close() error {
	s.once.Do(func() {
		s.closing.Store(true)
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.readers.Wait()
		select {
		case s.err = <-s.errors:
		default:
		}
	})
	return s.err
}
func startSubscribers(c config, nonce string) (*subscribers, error) {
	s := &subscribers{errors: make(chan error, c.Subscribers), expected: int64(c.Subscribers) * c.SubscriberDrainUpdates, timeout: c.Timeout}
	fail := func(err error) (*subscribers, error) { _ = s.Close(); return nil, err }
	symbols := []string{}
	seen := map[string]bool{}
	for id := 0; id < c.Concurrency; id++ {
		symbol := symbolFor(c, id)
		if !seen[symbol] {
			seen[symbol] = true
			symbols = append(symbols, symbol)
		}
	}
	for i := 0; i < c.Subscribers; i++ {
		conn, err := connect(c, 10000+i, nonce)
		if err != nil {
			return fail(err)
		}
		s.conns = append(s.conns, conn)
		err = sendFramed(conn, &protobuf.FXPMessage{Payload: &protobuf.FXPMessage_MarketDataRequest{MarketDataRequest: &protobuf.MarketDataRequest{Symbols: symbols}}})
		if err != nil {
			return fail(err)
		}
		for range symbols {
			snapshot, err := readFramed(conn)
			if err != nil {
				return fail(err)
			}
			if snapshot.GetMarketDataSnapshot() == nil {
				return fail(errors.New("expected subscription snapshot"))
			}
		}
		_ = conn.SetDeadline(time.Time{})
		s.readers.Add(1)
		go func() {
			defer s.readers.Done()
			for {
				m, err := readFramed(conn)
				if err != nil {
					if !s.closing.Load() {
						s.errors <- err
					}
					return
				}
				if m.GetMarketDataIncremental() != nil {
					s.updates.Add(1)
				}
				if m.GetErrorMessage() != nil {
					s.errors <- errors.New(m.GetErrorMessage().ErrorMessage)
					return
				}
			}
		}()
	}
	return s, nil
}

// Pipe protocol: parent sends configuration, waits for READY, then sends STOP.
// The child closes its own sockets, joins every reader and reports counts/errors.
type subscriberRequest struct {
	Config config
	Nonce  string
}
type subscriberReply struct {
	Ready   bool
	Updates int64
	Error   string
}

func runSubscriberWorker(input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	encoder := json.NewEncoder(output)
	var request subscriberRequest
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	group, err := startSubscribers(request.Config, request.Nonce)
	if err != nil {
		_ = encoder.Encode(subscriberReply{Error: err.Error()})
		return err
	}
	defer func() { _ = group.Close() }()
	if err = encoder.Encode(subscriberReply{Ready: true}); err != nil {
		return err
	}
	var stop string
	if err = decoder.Decode(&stop); err != nil {
		return err
	}
	if stop != "STOP" && stop != "FINISH" {
		return errors.New("invalid subscriber control")
	}
	if stop == "FINISH" {
		err = group.Finish()
	} else {
		err = group.Close()
	}
	reply := subscriberReply{Updates: group.Updates()}
	if err != nil {
		reply.Error = err.Error()
	}
	return encoder.Encode(reply)
}

type subscriberProcess struct {
	timeout time.Duration
	command *exec.Cmd
	input   io.WriteCloser
	decoder *json.Decoder
	once    sync.Once
	updates int64
	err     error
}

func (s *subscriberProcess) Updates() int64 { return s.updates }
func (s *subscriberProcess) Finish() error  { return s.closeWith("FINISH") }
func (s *subscriberProcess) Close() error   { return s.closeWith("STOP") }
func (s *subscriberProcess) closeWith(control string) error {
	s.once.Do(func() {
		timer := time.AfterFunc(s.timeout+5*time.Second, func() { _ = s.command.Process.Kill() })
		defer timer.Stop()
		err := json.NewEncoder(s.input).Encode(control)
		_ = s.input.Close()
		var reply subscriberReply
		if err == nil {
			err = s.decoder.Decode(&reply)
		}
		s.updates = reply.Updates
		if err == nil && reply.Error != "" {
			err = errors.New(reply.Error)
		}
		waitErr := s.command.Wait()
		if err == nil {
			err = waitErr
		}
		s.err = err
	})
	return s.err
}
func startSubscriberProcess(c config, nonce string) (subscriberHandle, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, "--subscriber-worker")
	if c.SubscriberCPU != "" {
		command = exec.Command("taskset", "-c", c.SubscriberCPU, executable, "--subscriber-worker")
	}
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	if err = command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	fail := func(err error) (subscriberHandle, error) {
		_ = input.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	timer := time.AfterFunc(c.Timeout, func() { _ = command.Process.Kill() })
	defer timer.Stop()
	if err = json.NewEncoder(input).Encode(subscriberRequest{c, nonce}); err != nil {
		return fail(err)
	}
	decoder := json.NewDecoder(output)
	var reply subscriberReply
	if err = decoder.Decode(&reply); err != nil {
		return fail(fmt.Errorf("subscriber startup: %w", err))
	}
	if !reply.Ready {
		return fail(fmt.Errorf("subscriber startup: %s", reply.Error))
	}
	return &subscriberProcess{command: command, input: input, decoder: decoder, timeout: c.Timeout}, nil
}
