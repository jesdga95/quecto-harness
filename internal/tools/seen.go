package tools

import (
	"os"
	"sync"
	"time"
)

// Seen records each file a session has read or written, as its size and
// modification time were then. It outlives the Runner, which is rebuilt every
// turn, so a file read in one turn can be edited in the next.
type Seen struct {
	mu    sync.Mutex
	files map[string]stamp
}

type stamp struct {
	size int64
	mod  time.Time
}

func NewSeen() *Seen {
	return &Seen{files: map[string]stamp{}}
}

// note records a file as the session now knows it.
func (s *Seen) note(path string, info os.FileInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = stamp{size: info.Size(), mod: info.ModTime()}
}

// check reports whether the session has read the file, and whether it is
// still as it was then.
func (s *Seen) check(path string, info os.FileInfo) (read, same bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.files[path]
	if !ok {
		return false, false
	}
	return true, st.size == info.Size() && st.mod.Equal(info.ModTime())
}

// noteFile records a file after the tools changed it. A file that cannot be
// read back is left as it was recorded, which the next check reports.
func (r *Runner) noteFile(path string) {
	if r.Seen == nil {
		return
	}
	if info, err := os.Stat(path); err == nil {
		r.Seen.note(path, info)
	}
}
