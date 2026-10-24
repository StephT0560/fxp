// SPDX-License-Identifier: MIT

package session

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileStore is a durable MessageStore for one FIX session, in its own directory:
//
//   - outbound.log — append-only records [len u32][crc32 u32][seq u64][frame],
//     big-endian, CRC (IEEE) over seq+frame. A torn or corrupt tail (crash
//     mid-write) is truncated on open.
//   - seqnums — "nextIn nextOut\n", replaced via write-temp + rename on every
//     change, so a reader never sees a partial file.
//
// Every write reaches the OS before Save / SetNextInSeq return, so a killed
// process loses nothing in any mode. FsyncMode only decides what survives a
// power or kernel failure (see FsyncMode).
//
// Retention: the newest MaxMessages frames are kept. When the log holds twice
// that, it is compacted to the newest MaxMessages. A ResendRequest for an
// older MsgSeqNum is answered with SequenceReset-GapFill, as for admin messages.
type FileStore struct {
	mu      sync.Mutex
	dir     string
	opts    FileStoreOptions
	log     *os.File
	index   map[int]recordLoc
	nextIn  int
	nextOut int
	dirty   bool // group mode: writes not yet fsynced
	stop    chan struct{}
	done    chan struct{}
	closed  bool
	failed  error // unrecoverable write failure, returned by every later write
}

// FsyncMode selects when FileStore calls fsync.
type FsyncMode int

const (
	// FsyncStrict fsyncs every record and seqnums update before returning.
	FsyncStrict FsyncMode = iota
	// FsyncGroup fsyncs at most every GroupInterval; a power failure can lose
	// up to that window.
	FsyncGroup
	// FsyncAsync never fsyncs; durability against power failure is up to the OS.
	FsyncAsync
)

func (m FsyncMode) String() string {
	return [...]string{"strict", "group", "async"}[m]
}

// ParseFsyncMode parses "strict" (default for ""), "group" or "async".
func ParseFsyncMode(s string) (FsyncMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "strict":
		return FsyncStrict, nil
	case "group":
		return FsyncGroup, nil
	case "async":
		return FsyncAsync, nil
	}
	return FsyncStrict, fmt.Errorf("unknown fsync mode %q (want strict, group or async)", s)
}

// FileStoreOptions configures a FileStore; zero values select the defaults.
type FileStoreOptions struct {
	Fsync         FsyncMode
	GroupInterval time.Duration // FsyncGroup flush interval (default 10ms)
	MaxMessages   int           // retained outbound frames (default 100000)
}

const (
	outboundLogName    = "outbound.log"
	seqNumsName        = "seqnums"
	recordHeaderLen    = 16
	maxRecordLen       = 16 << 20
	defaultMaxMessages = 100_000
	defaultGroupFlush  = 10 * time.Millisecond
)

type recordLoc struct {
	off int64 // offset of the frame bytes
	n   int
}

// OpenFileStore opens (creating if needed) the store in dir and recovers it.
func OpenFileStore(dir string, opts FileStoreOptions) (*FileStore, error) {
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = defaultMaxMessages
	}
	if opts.GroupInterval <= 0 {
		opts.GroupInterval = defaultGroupFlush
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, outboundLogName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	fs := &FileStore{dir: dir, opts: opts, log: f, index: make(map[int]recordLoc), nextIn: 1, nextOut: 1}
	maxSeq, err := fs.recover()
	if err == nil {
		err = fs.loadSeqNums(maxSeq)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open FIX store %s: %w", dir, err)
	}
	if opts.Fsync == FsyncGroup {
		fs.stop, fs.done = make(chan struct{}), make(chan struct{})
		go fs.groupFlusher()
	}
	return fs, nil
}

// recover indexes every intact record and truncates a torn or corrupt tail.
func (fs *FileStore) recover() (maxSeq int, err error) {
	if _, err := fs.log.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReader(fs.log)
	var off int64
	hdr := make([]byte, recordHeaderLen)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("FIX store %s: torn record header at offset %d — truncating", fs.dir, off)
			}
			break
		}
		n := binary.BigEndian.Uint32(hdr[0:4])
		if n > maxRecordLen {
			log.Printf("FIX store %s: bad record length at offset %d — truncating", fs.dir, off)
			break
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(r, frame); err != nil {
			log.Printf("FIX store %s: torn record at offset %d — truncating", fs.dir, off)
			break
		}
		if recordCRC(hdr[8:16], frame) != binary.BigEndian.Uint32(hdr[4:8]) {
			log.Printf("FIX store %s: CRC mismatch at offset %d — truncating", fs.dir, off)
			break
		}
		seq := int(binary.BigEndian.Uint64(hdr[8:16]))
		fs.index[seq] = recordLoc{off: off + recordHeaderLen, n: int(n)}
		maxSeq = max(maxSeq, seq)
		off += recordHeaderLen + int64(n)
	}
	if err := fs.log.Truncate(off); err != nil {
		return 0, err
	}
	_, err = fs.log.Seek(off, io.SeekStart)
	return maxSeq, err
}

func (fs *FileStore) loadSeqNums(maxSeq int) error {
	raw, err := os.ReadFile(filepath.Join(fs.dir, seqNumsName))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if _, err := fmt.Sscanf(string(raw), "%d %d", &fs.nextIn, &fs.nextOut); err != nil {
			return fmt.Errorf("corrupt %s: %w", seqNumsName, err)
		}
	}
	// The log is authoritative for outbound numbers already used.
	fs.nextOut = max(fs.nextOut, maxSeq+1)
	return nil
}

func recordCRC(seq, frame []byte) uint32 {
	return crc32.Update(crc32.ChecksumIEEE(seq), crc32.IEEETable, frame)
}

var errStoreClosed = errors.New("FIX store closed")

func (fs *FileStore) writableLocked() error {
	if fs.closed {
		return errStoreClosed
	}
	return fs.failed
}

func (fs *FileStore) Save(seq int, frame []byte) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.writableLocked(); err != nil {
		return err
	}
	off, err := fs.appendLocked(fs.log, seq, frame)
	if err != nil {
		return fmt.Errorf("FIX store %s: append seq %d: %w", fs.dir, seq, err)
	}
	fs.index[seq] = recordLoc{off: off, n: len(frame)}
	fs.nextOut = seq + 1
	if err := fs.syncLocked(fs.log); err != nil {
		return err
	}
	if err := fs.writeSeqNumsLocked(); err != nil {
		return err
	}
	if len(fs.index) >= 2*fs.opts.MaxMessages {
		// The old log stays valid if compaction fails, so only log it.
		if err := fs.compactLocked(); err != nil {
			log.Printf("FIX store %s: compact: %v", fs.dir, err)
		}
	}
	return nil
}

// appendLocked writes one record at the end of f and returns the frame offset.
func (fs *FileStore) appendLocked(f *os.File, seq int, frame []byte) (int64, error) {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	rec := make([]byte, recordHeaderLen+len(frame))
	binary.BigEndian.PutUint32(rec[0:4], uint32(len(frame)))
	binary.BigEndian.PutUint64(rec[8:16], uint64(seq))
	binary.BigEndian.PutUint32(rec[4:8], recordCRC(rec[8:16], frame))
	copy(rec[recordHeaderLen:], frame)
	if _, err := f.Write(rec); err != nil {
		// A partial record would hide every later one from recovery.
		if terr := f.Truncate(end); terr != nil && f == fs.log {
			fs.failed = fmt.Errorf("FIX store %s: partial record left in %s: %w", fs.dir, outboundLogName, terr)
		}
		return 0, err
	}
	return end + recordHeaderLen, nil
}

// syncLocked fsyncs f now (strict) or marks the store dirty (group). A failed
// fsync leaves the file state unknown, so the store refuses further writes.
func (fs *FileStore) syncLocked(f *os.File) error {
	switch fs.opts.Fsync {
	case FsyncStrict:
		if err := f.Sync(); err != nil {
			fs.failed = fmt.Errorf("FIX store %s: fsync: %w", fs.dir, err)
			return fs.failed
		}
	case FsyncGroup:
		fs.dirty = true
	}
	return nil
}

func (fs *FileStore) writeSeqNumsLocked() error {
	tmp := filepath.Join(fs.dir, seqNumsName+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = fmt.Fprintf(f, "%d %d\n", fs.nextIn, fs.nextOut)
		if err == nil && fs.opts.Fsync == FsyncStrict {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(fs.dir, seqNumsName))
	}
	if err != nil {
		return fmt.Errorf("FIX store %s: write %s: %w", fs.dir, seqNumsName, err)
	}
	if fs.opts.Fsync == FsyncStrict {
		fs.syncDir()
		return nil
	}
	return fs.syncLocked(nil)
}

func (fs *FileStore) syncDir() {
	if d, err := os.Open(fs.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// compactLocked rewrites the log with the newest MaxMessages records.
func (fs *FileStore) compactLocked() error {
	keepFrom := fs.nextOut - fs.opts.MaxMessages
	tmpPath := filepath.Join(fs.dir, outboundLogName+".tmp")
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	index := make(map[int]recordLoc, fs.opts.MaxMessages)
	for seq := keepFrom; seq < fs.nextOut; seq++ {
		frame, ok := fs.readLocked(seq)
		if !ok {
			continue
		}
		off, err := fs.appendLocked(tmp, seq, frame)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		index[seq] = recordLoc{off: off, n: len(frame)}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(fs.dir, outboundLogName)); err != nil {
		_ = tmp.Close()
		return err
	}
	fs.syncDir()
	_ = fs.log.Close()
	fs.log, fs.index = tmp, index
	return nil
}

func (fs *FileStore) readLocked(seq int) ([]byte, bool) {
	loc, ok := fs.index[seq]
	if !ok {
		return nil, false
	}
	frame := make([]byte, loc.n)
	if _, err := fs.log.ReadAt(frame, loc.off); err != nil {
		return nil, false
	}
	return frame, true
}

func (fs *FileStore) Get(seq int) ([]byte, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed || seq < fs.nextOut-fs.opts.MaxMessages {
		return nil, false
	}
	return fs.readLocked(seq)
}

func (fs *FileStore) SetNextInSeq(n int) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.writableLocked(); err != nil {
		return err
	}
	fs.nextIn = n
	return fs.writeSeqNumsLocked()
}

func (fs *FileStore) SeqNums() (int, int) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.nextIn, fs.nextOut
}

func (fs *FileStore) Reset() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return
	}
	if err := fs.log.Truncate(0); err != nil {
		log.Printf("FIX store %s: reset: %v", fs.dir, err)
	}
	fs.index = make(map[int]recordLoc)
	fs.nextIn, fs.nextOut = 1, 1
	if err := errors.Join(fs.syncLocked(fs.log), fs.writeSeqNumsLocked()); err != nil {
		log.Printf("FIX store %s: reset: %v", fs.dir, err)
	}
}

func (fs *FileStore) groupFlusher() {
	defer close(fs.done)
	t := time.NewTicker(fs.opts.GroupInterval)
	defer t.Stop()
	for {
		select {
		case <-fs.stop:
			return
		case <-t.C:
			fs.mu.Lock()
			if fs.dirty {
				fs.flushLocked()
			}
			fs.mu.Unlock()
		}
	}
}

func (fs *FileStore) flushLocked() {
	if err := fs.log.Sync(); err != nil {
		log.Printf("FIX store %s: fsync: %v", fs.dir, err)
		if fs.failed == nil {
			fs.failed = fmt.Errorf("FIX store %s: fsync: %w", fs.dir, err)
		}
	}
	if f, err := os.Open(filepath.Join(fs.dir, seqNumsName)); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	fs.syncDir()
	fs.dirty = false
}

// Close flushes (in every mode) and closes the store.
func (fs *FileStore) Close() error {
	fs.mu.Lock()
	if fs.closed {
		fs.mu.Unlock()
		return nil
	}
	fs.closed = true
	fs.flushLocked()
	err := fs.log.Close()
	fs.mu.Unlock()
	if fs.stop != nil {
		close(fs.stop)
		<-fs.done
	}
	return err
}
