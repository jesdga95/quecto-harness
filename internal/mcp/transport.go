package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

// transport carries frames to and from one server. Frames that arrive go to
// the channel Recv returns, which closes when the connection ends; Err then
// says why.
type transport interface {
	Send(ctx context.Context, frame []byte) error
	Recv() <-chan message
	Err() error
	Close() error
}

// maxFrame caps one incoming frame. A longer one is dropped and the reader
// carries on at the next line.
const maxFrame = 4 << 20

// readLines splits r on newlines and hands each complete line to fn. A line
// past maxFrame is skipped whole. It returns what ended the read.
func readLines(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	skipping := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			if !skipping {
				line = append(line, chunk...)
				if l := bytes.TrimSpace(line); len(l) > 0 {
					fn(l)
				}
			}
			line, skipping = line[:0], false
			continue
		case errors.Is(err, bufio.ErrBufferFull):
			if !skipping {
				line = append(line, chunk...)
				if len(line) > maxFrame {
					line, skipping = line[:0], true
				}
			}
			continue
		}
		if !skipping {
			line = append(line, chunk...)
			if l := bytes.TrimSpace(line); len(l) > 0 && len(l) <= maxFrame {
				fn(l)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}

// tail keeps the end of what a server wrote to stderr, which is usually the
// line that says why it failed.
type tail struct {
	mu  sync.Mutex
	buf []byte
	to  io.Writer
}

const tailBytes = 4096

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailBytes {
		t.buf = t.buf[len(t.buf)-tailBytes:]
	}
	t.mu.Unlock()
	if t.to != nil {
		_, _ = t.to.Write(p)
	}
	return len(p), nil
}

// lastLine is the last non-empty line written, empty when there was none.
func (t *tail) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := bytes.Split(bytes.TrimSpace(t.buf), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 {
			return string(l)
		}
	}
	return ""
}
