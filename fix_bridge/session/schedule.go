// SPDX-License-Identifier: MIT

package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // identical IANA timezone support in the minimal Docker image
)

// Schedule is a half-open local-time daily or weekly session window.
// Equal start/end means a full day/week, with a reset at each end boundary.
type Schedule struct {
	start, end       int
	startDay, endDay time.Weekday
	weekly           bool
	loc              *time.Location
}

func ParseSchedule(start, end, startDay, endDay, zone string) (*Schedule, error) {
	if start == "" && end == "" && startDay == "" && endDay == "" {
		return nil, nil
	}
	parse := func(s string) (int, error) {
		t, e := time.Parse("15:04:05", s)
		return t.Hour()*3600 + t.Minute()*60 + t.Second(), e
	}
	a, err := parse(start)
	if err != nil {
		return nil, fmt.Errorf("StartTime: %w", err)
	}
	b, err := parse(end)
	if err != nil {
		return nil, fmt.Errorf("EndTime: %w", err)
	}
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	s := &Schedule{start: a, end: b, loc: loc}
	if startDay != "" || endDay != "" {
		day := func(v string) (time.Weekday, error) {
			for d := time.Sunday; d <= time.Saturday; d++ {
				if strings.EqualFold(v, d.String()) {
					return d, nil
				}
			}
			return 0, fmt.Errorf("invalid weekday %q", v)
		}
		s.weekly = true
		s.startDay, err = day(startDay)
		if err != nil {
			return nil, err
		}
		s.endDay, err = day(endDay)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Schedule) at(date time.Time, seconds int) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), seconds/3600, seconds/60%60, seconds%60, 0, s.loc)
}
func (s *Schedule) previous(now time.Time, seconds int, day time.Weekday) time.Time {
	n := now.In(s.loc)
	date := n
	if s.weekly {
		date = date.AddDate(0, 0, -(int(n.Weekday())-int(day)+7)%7)
	}
	at := s.at(date, seconds)
	if at.After(now) {
		step := 1
		if s.weekly {
			step = 7
		}
		at = s.at(date.AddDate(0, 0, -step), seconds)
	}
	return at
}
func (s *Schedule) Contains(now time.Time) bool {
	if s == nil {
		return true
	}
	start := s.previous(now, s.start, s.startDay)
	end := s.previous(now, s.end, s.endDay)
	return start.After(end) || start.Equal(end) // equal boundaries begin the new full-length session
}
func (s *Schedule) ResetBoundary(now time.Time) time.Time { return s.previous(now, s.end, s.endDay) }

// ConfigureSchedule reads explicit FIX session settings; invalid values fail startup.
func (s *Session) ConfigureSchedule() error {
	var err error
	s.Schedule, err = ParseSchedule(os.Getenv("FXP_FIX_START_TIME"), os.Getenv("FXP_FIX_END_TIME"), os.Getenv("FXP_FIX_START_DAY"), os.Getenv("FXP_FIX_END_DAY"), os.Getenv("FXP_FIX_TIME_ZONE"))
	if err != nil {
		return err
	}
	for name, dest := range map[string]*bool{"FXP_FIX_DICTIONARY_STRICT": &s.StrictDictionary, "FXP_FIX_RESET_ON_LOGON": &s.ResetOnLogon, "FXP_FIX_RESET_ON_LOGOUT": &s.ResetOnLogout, "FXP_FIX_RESET_ON_DISCONNECT": &s.ResetOnDisconnect} {
		if v := os.Getenv(name); v != "" {
			b, e := strconv.ParseBool(v)
			if e != nil {
				return fmt.Errorf("%s: %w", name, e)
			}
			*dest = b
		}
	}
	return nil
}

// CheckSchedule can be driven by an injected clock without waiting on real time.
func (s *Session) CheckSchedule() {
	if s.Schedule == nil || s.State() != StateLoggedOn {
		return
	}
	if !s.Schedule.Contains(s.Now()) || s.Schedule.ResetBoundary(s.Now()).Unix() > s.scheduleEpoch.Load() {
		s.resetAtClose.Store(true)
		s.Logout("session schedule ended; sequence numbers reset")
		s.flush(flushTimeout)
		s.close()
	}
}
func (s *Session) scheduleLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.CheckSchedule()
		}
	}
}
func (s *Session) advanceSchedule() error {
	if s.Schedule == nil {
		return nil
	}
	epoch := s.Schedule.ResetBoundary(s.Now()).Unix()
	st, ok := s.Store.(interface{ AdvanceEpoch(int64) error })
	if !ok {
		return fmt.Errorf("store does not support scheduled resets")
	}
	if err := st.AdvanceEpoch(epoch); err != nil {
		return err
	}
	s.inSeqNum, _ = s.Store.SeqNums()
	_, out := s.Store.SeqNums()
	s.outSeqNum = int64(out)
	s.scheduleEpoch.Store(epoch)
	return nil
}
