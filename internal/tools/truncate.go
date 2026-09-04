package tools

import (
	"bytes"
	"strconv"
	"sync"
	"unicode/utf8"
)

// Truncate keeps the head and the tail of s within limit bytes, cutting on
// line boundaries where one is close enough. The marker is added on top of
// the limit rather than inside it, so it cannot itself be cut.
func Truncate(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	head := limit * 2 / 3
	tail := limit - head

	head = forwardToNewline(s, head)
	tailStart := backToNewline(s, len(s)-tail)
	if tailStart < head {
		tailStart = head
	}
	omitted := tailStart - head
	if omitted <= 0 {
		return s, false
	}
	return s[:head] + "\n... " + strconv.Itoa(omitted) + " bytes omitted ...\n" + s[tailStart:], true
}

// forwardToNewline moves a cut forward to the next line break within 512
// bytes, and otherwise back to a rune boundary.
func forwardToNewline(s string, at int) int {
	end := at + 512
	if end > len(s) {
		end = len(s)
	}
	if i := bytes.IndexByte([]byte(s[at:end]), '\n'); i >= 0 {
		return at + i + 1
	}
	for at > 0 && !utf8.RuneStart(s[at]) {
		at--
	}
	return at
}

// backToNewline moves a cut back to the previous line break within 512 bytes,
// and otherwise forward to a rune boundary.
func backToNewline(s string, at int) int {
	start := at - 512
	if start < 0 {
		start = 0
	}
	if i := bytes.LastIndexByte([]byte(s[start:at]), '\n'); i >= 0 {
		return start + i + 1
	}
	for at < len(s) && !utf8.RuneStart(s[at]) {
		at++
	}
	return at
}

// looksBinary reports whether the sniff is not text. A NUL byte settles it;
// otherwise invalid UTF-8 over one percent of the sniff does.
func looksBinary(sniff []byte) bool {
	if bytes.IndexByte(sniff, 0) >= 0 {
		return true
	}
	if len(sniff) == 0 {
		return false
	}
	end := len(sniff)
	for i := 0; i < 3 && end > 0; i++ {
		if r, size := utf8.DecodeLastRune(sniff[:end]); r == utf8.RuneError && size <= 1 {
			end--
			continue
		}
		break
	}
	bad := 0
	for i := 0; i < end; {
		r, size := utf8.DecodeRune(sniff[i:end])
		if r == utf8.RuneError && size == 1 {
			bad++
		}
		i += size
	}
	if bad == 0 {
		return false
	}
	if len(sniff) < 100 {
		return true
	}
	return bad > len(sniff)/100
}

// limitBuffer collects command output under a lock, because os/exec writes
// stdout and stderr from two goroutines into the same writer. It keeps a head
// and a tail so a command that floods the pipe cannot exhaust memory.
type limitBuffer struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte
	total int
	keep  int
}

func newLimitBuffer(capBytes int) *limitBuffer {
	keep := capBytes * 4
	if keep < 4096 {
		keep = 4096
	}
	return &limitBuffer{keep: keep}
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += len(p)
	if room := b.keep - len(b.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.head = append(b.head, p[:room]...)
	}
	b.tail = append(b.tail, p...)
	if len(b.tail) > b.keep {
		b.tail = append(b.tail[:0], b.tail[len(b.tail)-b.keep:]...)
	}
	return len(p), nil
}

// String rebuilds the output exactly while it fits in the head and the tail,
// and otherwise names how much was dropped in between.
func (b *limitBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total <= len(b.head) {
		return string(b.head)
	}
	missing := b.total - len(b.head)
	if missing <= len(b.tail) {
		return string(b.head) + string(b.tail[len(b.tail)-missing:])
	}
	omitted := b.total - len(b.head) - len(b.tail)
	return string(b.head) + "\n... " + strconv.Itoa(omitted) + " bytes omitted ...\n" + string(b.tail)
}

func (b *limitBuffer) Total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}
