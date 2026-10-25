// SPDX-License-Identifier: MIT

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *MemoryStore) AdvanceEpoch(epoch int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch < epoch {
		m.msgs = make(map[int][]byte)
		m.nextIn, m.nextOut = 1, 1
		m.epoch = epoch
	}
	return nil
}

// AdvanceEpoch archives before resetting. A durable intent marker makes a
// crash during reset recoverable, preventing old log frames from resurrecting.
func (fs *FileStore) AdvanceEpoch(epoch int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.writableLocked(); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(fs.dir, "epoch"))
	var old int64
	if err == nil {
		old, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if old >= epoch {
		return nil
	}
	archive := filepath.Join(fs.dir, "archive", strconv.FormatInt(epoch, 10))
	if err := os.MkdirAll(archive, 0700); err != nil {
		return err
	}
	for _, name := range []string{outboundLogName, seqNumsName} {
		raw, err := os.ReadFile(filepath.Join(fs.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := durableFile(filepath.Join(archive, name), raw); err != nil {
			return err
		}
	}
	if err := durableFile(filepath.Join(fs.dir, "reset.pending"), []byte(strconv.FormatInt(epoch, 10))); err != nil {
		return err
	}
	if err := fs.finishEpochReset(epoch); err != nil {
		fs.failed = err
		return err
	}
	return nil
}
func durableFile(path string, data []byte) error {
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
func (fs *FileStore) finishEpochReset(epoch int64) error {
	if err := fs.log.Truncate(0); err != nil {
		return err
	}
	if err := fs.log.Sync(); err != nil {
		return err
	}
	fs.index = make(map[int]recordLoc)
	fs.nextIn, fs.nextOut = 1, 1
	if err := durableFile(filepath.Join(fs.dir, seqNumsName), []byte("1 1\n")); err != nil {
		return err
	}
	if err := durableFile(filepath.Join(fs.dir, "epoch"), []byte(fmt.Sprint(epoch))); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(fs.dir, "reset.pending")); err != nil {
		return err
	}
	fs.syncDir()
	return nil
}
