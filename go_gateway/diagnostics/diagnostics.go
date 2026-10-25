// SPDX-License-Identifier: MIT
// Package diagnostics provides explicitly enabled, local-file profiling only.
package diagnostics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go_gateway/protobuf"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"sync"
	"sync/atomic"
)

type sink struct {
	sync.Mutex
	writer *bufio.Writer
}

var active atomic.Pointer[sink]

func Enabled() bool { return active.Load() != nil }
func Event(stage, id string) {
	s := active.Load()
	if s == nil || id == "" {
		return
	}
	ns := monotonicNS()
	s.Lock()
	defer s.Unlock()
	_ = json.NewEncoder(s.writer).Encode(struct {
		Stage string `json:"stage"`
		ID    string `json:"id"`
		NS    int64  `json:"ns"`
	}{stage, id, ns})
}

// Message traces only new orders and their ACKs; no token or payload is logged.
func Message(stage string, m *protobuf.FXPMessage) {
	if !Enabled() {
		return
	}
	if o := m.GetNewOrder(); o != nil {
		Event(stage, o.OrderId)
	}
	if r := m.GetExecutionReport(); r != nil && r.ExecutionType == protobuf.ExecutionType_EXEC_NEW {
		Event(stage, r.OrderId)
	}
}
func Raw(stage string, raw []byte) {
	if !Enabled() {
		return
	}
	m := new(protobuf.FXPMessage)
	if proto.Unmarshal(raw, m) == nil {
		Message(stage, m)
	}
}

// Start is called once before serving. Files contain sensitive order IDs and use
// mode 0600. Profile collection has overhead; never mix it into baseline runs.
func Start() (func(), error) {
	var closers []func()
	stop := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	fail := func(err error) (func(), error) { stop(); return nil, err }
	if path := os.Getenv("FXP_HOP_TRACE"); path != "" {
		if monotonicNS() == 0 {
			return fail(fmt.Errorf("CLOCK_MONOTONIC unavailable"))
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		s := &sink{writer: bufio.NewWriterSize(f, 256*1024)}
		active.Store(s)
		closers = append(closers, func() { active.Store(nil); s.Lock(); defer s.Unlock(); _ = s.writer.Flush(); _ = f.Close() })
	}
	if dir := os.Getenv("FXP_PROFILE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fail(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, "cpu.pprof"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		if err = pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			return fail(err)
		}
		runtime.SetBlockProfileRate(1)
		runtime.SetMutexProfileFraction(1)
		closers = append(closers, func() {
			pprof.StopCPUProfile()
			_ = f.Close()
			for _, name := range []string{"allocs", "block", "mutex"} {
				if f, err := os.OpenFile(filepath.Join(dir, name+".pprof"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600); err == nil {
					_ = pprof.Lookup(name).WriteTo(f, 0)
					_ = f.Close()
				}
			}
			runtime.SetBlockProfileRate(0)
			runtime.SetMutexProfileFraction(0)
		})
	}
	if path := os.Getenv("FXP_TRACE_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return fail(err)
		}
		if err = trace.Start(f); err != nil {
			_ = f.Close()
			return fail(err)
		}
		closers = append(closers, func() { trace.Stop(); _ = f.Close() })
	}
	return stop, nil
}
