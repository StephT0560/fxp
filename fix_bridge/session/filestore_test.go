// SPDX-License-Identifier: MIT

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"fix_bridge/codec"
)

var allFsyncModes = []FsyncMode{FsyncStrict, FsyncGroup, FsyncAsync}

func openStore(t *testing.T, dir string, opts FileStoreOptions) *FileStore {
	t.Helper()
	fs, err := OpenFileStore(dir, opts)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	return fs
}

func frameN(n int) []byte { return []byte(fmt.Sprintf("frame-%d", n)) }

func TestParseFsyncMode(t *testing.T) {
	for in, want := range map[string]FsyncMode{"": FsyncStrict, "strict": FsyncStrict, "group": FsyncGroup, "async": FsyncAsync} {
		if got, err := ParseFsyncMode(in); err != nil || got != want {
			t.Fatalf("ParseFsyncMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseFsyncMode("sometimes"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestFileStoreSurvivesReopenAndAbruptClose(t *testing.T) {
	for _, mode := range allFsyncModes {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			fs := openStore(t, dir, FileStoreOptions{Fsync: mode})
			for i := 1; i <= 5; i++ {
				fs.Save(i, frameN(i))
			}
			fs.SetNextInSeq(7)
			// No Close: simulates the process being killed (data reached the OS).
			re := openStore(t, dir, FileStoreOptions{Fsync: mode})
			defer re.Close()
			if in, out := re.SeqNums(); in != 7 || out != 6 {
				t.Fatalf("SeqNums = %d,%d; want 7,6", in, out)
			}
			for i := 1; i <= 5; i++ {
				if f, ok := re.Get(i); !ok || string(f) != string(frameN(i)) {
					t.Fatalf("Get(%d) = %q,%v", i, f, ok)
				}
			}
			fs.Close()
		})
	}
}

func TestFileStoreRecoversTornTail(t *testing.T) {
	dir := t.TempDir()
	fs := openStore(t, dir, FileStoreOptions{})
	for i := 1; i <= 3; i++ {
		fs.Save(i, frameN(i))
	}
	fs.Close()
	logPath := filepath.Join(dir, outboundLogName)
	whole, _ := os.ReadFile(logPath)

	// Half-written header after the last record.
	f, _ := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.Write([]byte{0, 0, 0, 9, 1, 2})
	f.Close()
	re := openStore(t, dir, FileStoreOptions{})
	if _, out := re.SeqNums(); out != 4 {
		t.Fatalf("next out after torn header = %d, want 4", out)
	}
	re.Save(4, frameN(4))
	re.Close()
	re = openStore(t, dir, FileStoreOptions{})
	if f, ok := re.Get(4); !ok || string(f) != "frame-4" {
		t.Fatalf("record appended after truncation lost: %q %v", f, ok)
	}
	re.Close()

	// Corrupt the payload of record 3: its CRC fails, so it and everything after is dropped.
	whole[len(whole)-1] ^= 0xFF
	_ = os.WriteFile(logPath, whole, 0o600)
	_ = os.Remove(filepath.Join(dir, seqNumsName))
	re = openStore(t, dir, FileStoreOptions{})
	defer re.Close()
	if _, ok := re.Get(3); ok {
		t.Fatal("record with bad CRC served")
	}
	if _, ok := re.Get(2); !ok {
		t.Fatal("good record before corruption lost")
	}
	if _, out := re.SeqNums(); out != 3 {
		t.Fatalf("next out = %d, want 3", out)
	}
}

func TestFileStoreRetentionCapsLog(t *testing.T) {
	dir := t.TempDir()
	fs := openStore(t, dir, FileStoreOptions{MaxMessages: 10})
	for i := 1; i <= 95; i++ {
		fs.Save(i, frameN(i))
	}
	fs.Close()
	fs = openStore(t, dir, FileStoreOptions{MaxMessages: 10})
	defer fs.Close()
	if _, ok := fs.Get(1); ok {
		t.Fatal("record past retention still served")
	}
	for i := 86; i <= 95; i++ {
		if _, ok := fs.Get(i); !ok {
			t.Fatalf("retained record %d missing", i)
		}
	}
	if _, out := fs.SeqNums(); out != 96 {
		t.Fatalf("next out = %d, want 96", out)
	}
	st, _ := os.Stat(filepath.Join(dir, outboundLogName))
	if max := int64(20 * (recordHeaderLen + len("frame-95"))); st.Size() > max {
		t.Fatalf("log size %d exceeds 2*retention bound %d", st.Size(), max)
	}
}

func TestFileStoreReset(t *testing.T) {
	dir := t.TempDir()
	fs := openStore(t, dir, FileStoreOptions{})
	fs.Save(1, frameN(1))
	fs.SetNextInSeq(9)
	fs.Reset()
	fs.Close()
	fs = openStore(t, dir, FileStoreOptions{})
	defer fs.Close()
	if in, out := fs.SeqNums(); in != 1 || out != 1 {
		t.Fatalf("SeqNums after Reset = %d,%d", in, out)
	}
	if _, ok := fs.Get(1); ok {
		t.Fatal("message survived Reset")
	}
}

func TestFileStoreConcurrentSessions(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(FileStoreFactory(root, FileStoreOptions{Fsync: FsyncGroup}))
	var wg sync.WaitGroup
	for c := 0; c < 8; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			key := SessionKey{"FIX.4.4", "FXPBRIDGE", fmt.Sprintf("C%d", c)}
			st, err := reg.Acquire(key)
			if err != nil {
				t.Error(err)
				return
			}
			for i := 1; i <= 50; i++ {
				st.Save(i, []byte(fmt.Sprintf("%d-%d", c, i)))
				st.SetNextInSeq(i + 1)
			}
			reg.Release(key)
		}(c)
	}
	wg.Wait()
	reg.Close()
	reg = NewRegistry(FileStoreFactory(root, FileStoreOptions{}))
	defer reg.Close()
	for c := 0; c < 8; c++ {
		st, err := reg.Acquire(SessionKey{"FIX.4.4", "FXPBRIDGE", fmt.Sprintf("C%d", c)})
		if err != nil {
			t.Fatal(err)
		}
		if in, out := st.SeqNums(); in != 51 || out != 51 {
			t.Fatalf("C%d SeqNums = %d,%d", c, in, out)
		}
		if f, _ := st.Get(50); string(f) != fmt.Sprintf("%d-50", c) {
			t.Fatalf("C%d frame 50 = %q", c, f)
		}
	}
}

// ── Session-level: state keyed by CompIDs survives reconnects ────────────────

func registryPeer(t *testing.T, reg *Registry, sender string) *peer {
	p := newPeerConfigured(t, func(s *Session) {
		s.Registry = reg
		s.Auth = NewCredentialAuth(map[string]string{"CLIENT": testPassword, "CLIENT2": testPassword})
	})
	p.sender = sender
	return p
}

func (p *peer) logonAt(seq int) *codec.Message {
	p.t.Helper()
	p.seq = seq
	p.sendSeq(codec.MsgTypeLogon, seq, "98", "0", "108", "30", "554", testPassword)
	return p.expect(codec.MsgTypeLogon)
}

func appFrame(s *Session, id string) {
	s.SendWithSeq(func(n int) []byte {
		b := codec.NewBuilder(codec.MsgTypeExecutionReport, s.SenderCompID, s.TargetCompID, n)
		b.Set(codec.TagClOrdID, id)
		return b.Build()
	})
}

func disconnect(t *testing.T, p *peer, reg *Registry) {
	t.Helper()
	p.conn.Close()
	key := SessionKey{"FIX.4.4", "FXPBRIDGE", p.sender}
	waitFor(t, "session release", func() bool { return !reg.isActive(key) })
}

func testReconnect(t *testing.T, reg *Registry) {
	p := registryPeer(t, reg, "CLIENT")
	if got := p.logonAt(1).GetInt(codec.TagMsgSeqNum); got != 1 {
		t.Fatalf("first Logon ack 34=%d", got)
	}
	appFrame(p.sess, "ER1") // out 2
	p.expect(codec.MsgTypeExecutionReport)
	p.send(codec.MsgTypeTestRequest, "112", "T1") // in 2 → Heartbeat out 3
	p.expect(codec.MsgTypeHeartbeat)
	disconnect(t, p, reg)

	q := registryPeer(t, reg, "CLIENT")
	ack := q.logonAt(3)
	if got := ack.GetInt(codec.TagMsgSeqNum); got != 4 {
		t.Fatalf("Logon ack after reconnect 34=%d, want 4", got)
	}
	q.send(codec.MsgTypeResendRequest, "7", "2", "16", "2")
	re := q.expect(codec.MsgTypeExecutionReport)
	if re.GetInt(codec.TagMsgSeqNum) != 2 || re.Get(codec.TagPossDupFlag) != "Y" || re.Get(codec.TagClOrdID) != "ER1" {
		t.Fatalf("resend after reconnect = 34=%d 43=%q 11=%q", re.GetInt(codec.TagMsgSeqNum), re.Get(codec.TagPossDupFlag), re.Get(codec.TagClOrdID))
	}
	// A stale MsgSeqNum is still too low on the new connection.
	disconnect(t, q, reg)
	r := registryPeer(t, reg, "CLIENT")
	r.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "554", testPassword)
	if lo := r.expect(codec.MsgTypeLogout); lo.GetInt(codec.TagMsgSeqNum) != 5 {
		t.Fatalf("too-low Logon answered with 34=%d, want 5", lo.GetInt(codec.TagMsgSeqNum))
	}
}

func TestReconnectPreservesSeqNumsMemory(t *testing.T) {
	testReconnect(t, NewRegistry(MemoryStoreFactory))
}

func TestReconnectPreservesSeqNumsFile(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(FileStoreFactory(root, FileStoreOptions{}))
	defer reg.Close()
	testReconnect(t, reg)
}

func TestRestartPreservesSeqNumsAndResend(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(FileStoreFactory(root, FileStoreOptions{Fsync: FsyncAsync}))
	p := registryPeer(t, reg, "CLIENT")
	p.logonAt(1)
	appFrame(p.sess, "ER1")
	p.expect(codec.MsgTypeExecutionReport)
	// Simulated kill: the registry and its files are abandoned without Close.
	p.conn.Close()
	p.expectClosed()

	reg2 := NewRegistry(FileStoreFactory(root, FileStoreOptions{}))
	defer reg2.Close()
	q := registryPeer(t, reg2, "CLIENT")
	if got := q.logonAt(2).GetInt(codec.TagMsgSeqNum); got != 3 {
		t.Fatalf("Logon ack after restart 34=%d, want 3", got)
	}
	q.send(codec.MsgTypeResendRequest, "7", "1", "16", "0")
	gap := q.expect(codec.MsgTypeSequenceReset) // Logon (1) is admin → GapFill
	if gap.GetInt(codec.TagNewSeqNo) != 2 {
		t.Fatalf("GapFill NewSeqNo=%d, want 2", gap.GetInt(codec.TagNewSeqNo))
	}
	if re := q.expect(codec.MsgTypeExecutionReport); re.Get(codec.TagClOrdID) != "ER1" {
		t.Fatalf("resent 11=%q", re.Get(codec.TagClOrdID))
	}
}

func TestConcurrentLogonSameCompIDRejected(t *testing.T) {
	reg := NewRegistry(MemoryStoreFactory)
	p := registryPeer(t, reg, "CLIENT")
	p.logonAt(1)
	other := registryPeer(t, reg, "CLIENT2")
	other.logonAt(1) // independent session logs on alongside
	dup := registryPeer(t, reg, "CLIENT")
	dup.sendSeq(codec.MsgTypeLogon, 2, "98", "0", "108", "30", "554", testPassword)
	if lo := dup.expect(codec.MsgTypeLogout); lo.Get(codec.TagText) != ErrSessionActive.Error() {
		t.Fatalf("duplicate Logon 58=%q", lo.Get(codec.TagText))
	}
	dup.expectClosed()
	// The live session is untouched.
	p.send(codec.MsgTypeTestRequest, "112", "T")
	if hb := p.expect(codec.MsgTypeHeartbeat); hb.GetInt(codec.TagMsgSeqNum) != 2 {
		t.Fatalf("live session Heartbeat 34=%d, want 2", hb.GetInt(codec.TagMsgSeqNum))
	}
}
