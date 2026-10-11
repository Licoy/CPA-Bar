package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Retention is how long records are kept.
	Retention = 30 * 24 * time.Hour
	// MaxRecords bounds the records kept per instance, the oldest going
	// first.
	MaxRecords = 200_000
)

// store keeps the records of each instance in memory, oldest first, and in
// a JSON-lines file per instance that only grows by appending until it is
// rewritten without the expired records.
type store struct {
	dir string
	now func() time.Time

	// ver changes whenever the records change.
	ver atomic.Uint64

	mu   sync.Mutex
	logs map[string]*recordLog
}

type recordLog struct {
	recs []Record
	// lines is how many records the file holds, expired ones included.
	lines int
	f     *os.File
	w     *bufio.Writer
}

func newStore(dir string, now func() time.Time) *store {
	return &store{dir: dir, now: now, logs: map[string]*recordLog{}}
}

func (s *store) path(id string) string {
	return filepath.Join(s.dir, id+".jsonl")
}

// log loads the records of an instance on first use. Caller holds s.mu.
func (s *store) log(id string) *recordLog {
	if l, ok := s.logs[id]; ok {
		return l
	}
	l := &recordLog{}
	s.logs[id] = l
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return l
	}
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		l.lines++
		var r Record
		if json.Unmarshal(line, &r) == nil && !r.Time.IsZero() {
			l.recs = append(l.recs, r)
		}
	}
	slices.SortStableFunc(l.recs, func(a, b Record) int { return a.Time.Compare(b.Time) })
	s.prune(id, l)
	return l
}

// prune drops what is past the retention or over the cap, rewriting the
// file once it holds a quarter more lines than records. Caller holds s.mu.
func (s *store) prune(id string, l *recordLog) {
	cut := s.now().Add(-Retention)
	i, _ := slices.BinarySearchFunc(l.recs, cut, func(r Record, t time.Time) int { return r.Time.Compare(t) })
	i = max(i, len(l.recs)-MaxRecords)
	if i > 0 {
		l.recs = slices.Clone(l.recs[i:])
		s.ver.Add(1)
	}
	if l.lines > len(l.recs)+len(l.recs)/4+64 {
		_ = s.rewrite(id, l)
	}
}

// rewrite replaces the file with the kept records. Caller holds s.mu.
func (s *store) rewrite(id string, l *recordLog) error {
	s.closeFile(l)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, id+".*.tmp")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	for _, r := range l.recs {
		if err := enc.Encode(r); err != nil {
			break
		}
	}
	err = errors.Join(w.Flush(), tmp.Chmod(0o600), tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), s.path(id))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	l.lines = len(l.recs)
	return nil
}

func (s *store) closeFile(l *recordLog) {
	if l.f != nil {
		_ = l.w.Flush()
		_ = l.f.Close()
		l.f, l.w = nil, nil
	}
}

// add appends records of an instance. They are written to the buffer at
// once and reach the file at the next flush.
func (s *store) add(id string, recs ...Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.log(id)
	for _, r := range recs {
		// Records arrive in order; one that does not goes in its place.
		if n := len(l.recs); n > 0 && r.Time.Before(l.recs[n-1].Time) {
			i, _ := slices.BinarySearchFunc(l.recs, r.Time, func(x Record, t time.Time) int {
				if x.Time.After(t) {
					return 1
				}
				return -1
			})
			l.recs = slices.Insert(l.recs, i, r)
		} else {
			l.recs = append(l.recs, r)
		}
	}
	if l.f == nil {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.path(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		l.f, l.w = f, bufio.NewWriter(f)
	}
	s.ver.Add(1)
	enc := json.NewEncoder(l.w)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return err
		}
		l.lines++
	}
	if len(l.recs) > MaxRecords+MaxRecords/10 {
		s.prune(id, l)
	}
	return nil
}

// flush writes the buffered records of every instance, and drops the
// expired ones.
func (s *store) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range s.logs {
		if l.w != nil {
			_ = l.w.Flush()
		}
		s.prune(id, l)
	}
}

// read calls fn with the records of an instance, oldest first. fn must not
// keep the slice.
func (s *store) read(id string, fn func([]Record)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.log(id).recs)
}

// clear deletes the records of an instance.
func (s *store) clear(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.logs[id]; ok {
		s.closeFile(l)
		delete(s.logs, id)
	}
	s.ver.Add(1)
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *store) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.logs {
		s.closeFile(l)
	}
}
