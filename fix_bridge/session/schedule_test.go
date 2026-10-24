// SPDX-License-Identifier: MIT

package session

import (
	"fix_bridge/codec"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, start, end, sd, ed, zone, at string
		open                               bool
	}{
		{"before", "09:00:00", "17:00:00", "", "", "UTC", "2026-03-09T08:59:59Z", false},
		{"start", "09:00:00", "17:00:00", "", "", "UTC", "2026-03-09T09:00:00Z", true},
		{"end", "09:00:00", "17:00:00", "", "", "UTC", "2026-03-09T17:00:00Z", false},
		{"overnight", "22:00:00", "06:00:00", "", "", "UTC", "2026-03-09T02:00:00Z", true},
		{"overnight end", "22:00:00", "06:00:00", "", "", "UTC", "2026-03-09T06:00:00Z", false},
		{"DST spring before", "09:00:00", "17:00:00", "", "", "America/New_York", "2026-03-06T14:00:00Z", true},
		{"DST spring after", "09:00:00", "17:00:00", "", "", "America/New_York", "2026-03-09T13:00:00Z", true},
		{"DST fall", "09:00:00", "17:00:00", "", "", "America/New_York", "2026-11-02T14:00:00Z", true},
		{"weekly start", "22:00:00", "22:00:00", "Sunday", "Friday", "UTC", "2026-03-08T22:00:00Z", true},
		{"weekly middle", "22:00:00", "22:00:00", "Sunday", "Friday", "UTC", "2026-03-11T01:00:00Z", true},
		{"weekly end", "22:00:00", "22:00:00", "Sunday", "Friday", "UTC", "2026-03-13T22:00:00Z", false},
		{"weekly weekend", "22:00:00", "22:00:00", "Sunday", "Friday", "UTC", "2026-03-14T12:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSchedule(tc.start, tc.end, tc.sd, tc.ed, tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			at, _ := time.Parse(time.RFC3339, tc.at)
			if got := s.Contains(at); got != tc.open {
				t.Fatalf("Contains(%s)=%v want %v", tc.at, got, tc.open)
			}
		})
	}
}

func TestScheduleInvalidConfig(t *testing.T) {
	for _, args := range [][5]string{{"09:00:00", "", "", "", ""}, {"25:00:00", "17:00:00", "", "", ""}, {"09:00:00", "17:00:00", "Monday", "", ""}, {"09:00:00", "17:00:00", "Bad", "Friday", ""}, {"09:00:00", "17:00:00", "", "", "Bad/Zone"}} {
		if _, err := ParseSchedule(args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestScheduledStoreResetSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenFileStore(dir, FileStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	schedule, _ := ParseSchedule("09:00:00", "17:00:00", "", "", "UTC")
	before := time.Date(2026, 3, 9, 16, 59, 59, 0, time.UTC)
	if err := st.AdvanceEpoch(schedule.ResetBoundary(before).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(7, []byte("old frame")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNextInSeq(8); err != nil {
		t.Fatal(err)
	}
	// Reconnect right before the boundary must retain state.
	if err := st.AdvanceEpoch(schedule.ResetBoundary(before).Unix()); err != nil {
		t.Fatal(err)
	}
	if in, out := st.SeqNums(); in != 8 || out != 8 {
		t.Fatalf("before reset: %d %d", in, out)
	}
	if err := st.AdvanceEpoch(schedule.ResetBoundary(before.Add(time.Second)).Unix()); err != nil {
		t.Fatal(err)
	}
	if in, out := st.SeqNums(); in != 1 || out != 1 {
		t.Fatalf("after reset: %d %d", in, out)
	}
	if _, ok := st.Get(7); ok {
		t.Fatal("old frame retained")
	}
	st.Close()
	st, err = OpenFileStore(dir, FileStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if in, out := st.SeqNums(); in != 1 || out != 1 {
		t.Fatalf("reopened: %d %d", in, out)
	}
	files, err := filepath.Glob(filepath.Join(dir, "archive", "*", "outbound.log"))
	if err != nil || len(files) != 2 {
		t.Fatalf("archives %v, %v", files, err)
	}
	raw, err := os.ReadFile(files[1])
	if err != nil || len(raw) == 0 {
		t.Fatalf("archive missing old frame: %v", err)
	}
}

func TestSessionScheduleLogoutAndReconnect(t *testing.T) {
	var clock atomic.Int64
	at := time.Date(2026, 3, 9, 16, 59, 59, 0, time.UTC)
	clock.Store(at.Unix())
	sched, _ := ParseSchedule("09:00:00", "17:00:00", "", "", "UTC")
	reg := NewRegistry(FileStoreFactory(t.TempDir(), FileStoreOptions{}))
	defer reg.Close()
	configure := func(s *Session) {
		s.Schedule = sched
		s.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
		s.MaxLatency = 0
		s.Registry = reg
	}
	p := newPeerConfigured(t, configure)
	p.logon()
	clock.Store(at.Add(time.Second).Unix())
	p.sess.CheckSchedule()
	m := p.expect(codec.MsgTypeLogout)
	if !strings.Contains(m.Get(codec.TagText), "schedule ended") {
		t.Fatal(m.Get(codec.TagText))
	}
	key := SessionKey{"FIX.4.4", "FXPBRIDGE", "CLIENT"}
	deadline := time.Now().Add(time.Second)
	for reg.isActive(key) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if reg.isActive(key) {
		t.Fatal("store not released")
	}
	// Outside the window, even a correctly authenticated Logon is explicitly refused.
	q := newPeerConfigured(t, configure)
	q.sendSeq(codec.MsgTypeLogon, 1, "98", "0", "108", "30", "554", testPassword)
	if m := q.expect(codec.MsgTypeLogout); !strings.Contains(m.Get(codec.TagText), "outside") {
		t.Fatal(m.Get(codec.TagText))
	}
	clock.Store(at.AddDate(0, 0, 1).Add(-8*time.Hour + time.Second).Unix())
	r := newPeerConfigured(t, configure)
	if ack := r.logon(); ack.GetInt(codec.TagMsgSeqNum) != 1 {
		t.Fatal("next window did not start at 1")
	}
}

func TestSessionResetPolicies(t *testing.T) {
	for _, policy := range []string{"logon", "logout", "disconnect"} {
		t.Run(policy, func(t *testing.T) {
			store := NewMemoryStore()
			if policy == "logon" {
				store.Save(9, []byte("prior"))
				store.SetNextInSeq(10)
			}
			p := newPeerConfigured(t, func(s *Session) {
				s.Store = store
				s.inSeqNum, s.outSeqNum = func() (int, int64) { a, b := store.SeqNums(); return a, int64(b) }()
				s.ResetOnLogon = policy == "logon"
				s.ResetOnLogout = policy == "logout"
				s.ResetOnDisconnect = policy == "disconnect"
			})
			p.logon()
			if policy == "logon" {
				if in, out := store.SeqNums(); in != 2 || out != 2 {
					t.Fatalf("%d %d", in, out)
				}
				return
			}
			if policy == "logout" {
				p.send(codec.MsgTypeLogout)
				p.expect(codec.MsgTypeLogout)
			} else {
				p.conn.Close()
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				in, out := store.SeqNums()
				if in == 1 && out == 1 {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("policy did not reset store")
		})
	}
}
