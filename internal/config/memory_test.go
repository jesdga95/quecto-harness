package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMemoryConcurrentWriters(t *testing.T) {
	s := openStore(t)
	editor, err := s.Memory()
	if err != nil || editor.Text != "" {
		t.Fatalf("empty memory: %+v, %v", editor, err)
	}
	other, err := Open(s.Paths())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- s.Remember("First") }()
	go func() { done <- other.Remember("Second") }()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReplaceMemory("Stale", editor.Revision); !errors.Is(err, ErrMemoryChanged) {
		t.Fatalf("stale save: %v", err)
	}
	m, err := s.Memory()
	if err != nil || (m.Text != "First\nSecond\n" && m.Text != "Second\nFirst\n") {
		t.Fatalf("notes: %+v, %v", m, err)
	}
	info, err := os.Stat(s.Paths().Memory)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
}

func TestMemoryByteLimitAndClearing(t *testing.T) {
	s := openStore(t)
	m, err := s.Memory()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("é", 2047) + "x"
	m, err = s.ReplaceMemory(line+"\r\n", m.Revision)
	if err != nil || m.Text != line+"\n" || len(m.Text) != 4096 {
		t.Fatalf("full memory: %d bytes, %v", len(m.Text), err)
	}
	if _, err = s.ReplaceMemory(line+"y", m.Revision); err == nil {
		t.Fatal("accepted oversized memory")
	}
	for _, bad := range []string{"", "\t", "two\nlines", "too much"} {
		if err := s.Remember(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	current, err := s.Memory()
	if err != nil || current != m {
		t.Fatalf("failed write changed memory: %v", err)
	}
	m, err = s.ReplaceMemory("", m.Revision)
	if err != nil || m.Text != "" {
		t.Fatalf("clear: %+v, %v", m, err)
	}
	b, err := os.ReadFile(s.Paths().Memory)
	if err != nil || len(b) != 0 {
		t.Fatalf("cleared file: %q, %v", b, err)
	}
}

func TestMemoryNormalizesExistingCRLF(t *testing.T) {
	s := openStore(t)
	if err := os.WriteFile(s.Paths().Memory, []byte("Note\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory()
	if err != nil || m.Text != "Note\n" {
		t.Fatal(m, err)
	}
}

func TestOversizedMemoryWarning(t *testing.T) {
	s := openStore(t)
	if err := os.WriteFile(s.Paths().Memory, []byte(strings.Repeat("x", 2*memoryLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Memory(); err == nil || err.Error() != "Memory exceeds 4096 bytes." {
		t.Fatalf("read warning: %v", err)
	}
	m, err := s.EditableMemory()
	if err != nil || !strings.Contains(m.Warning, "Only the beginning is shown") {
		t.Fatalf("editor warning: %q, %v", m.Warning, err)
	}
}

func TestManualMemoryWithoutFinalNewline(t *testing.T) {
	s := openStore(t)
	raw := "First\r\nLast"
	if err := os.WriteFile(s.Paths().Memory, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory()
	if err != nil || m.Text != "First\nLast\n" || m.Revision != memoryValue(raw).Revision {
		t.Fatalf("manual memory: %+v, %v", m, err)
	}
	if err := s.Remember("Next"); err != nil {
		t.Fatal(err)
	}
	m, err = s.Memory()
	if err != nil || m.Text != "First\nLast\nNext\n" {
		t.Fatal(m, err)
	}
}

func TestRememberRejectsLineSeparatorsAndControls(t *testing.T) {
	s := openStore(t)
	for _, r := range "\x00\t\x1b\x1f\x7f\u009f\u200b\ufeff\u200d\r\n\v\f\u0085\u2028\u2029\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069" {
		for _, text := range []string{string(r) + "A", "A" + string(r) + "B", "A" + string(r)} {
			if err := s.Remember(text); err == nil {
				t.Fatalf("accepted %q", text)
			}
		}
	}
	if err := s.Remember("  Note  "); err != nil {
		t.Fatal(err)
	}
	m, err := s.Memory()
	if err != nil || m.Text != "Note\n" {
		t.Fatal(m, err)
	}
}

func TestReadManualMemoryAtByteLimit(t *testing.T) {
	s := openStore(t)
	raw := strings.Repeat("é", memoryLimit/2)
	if err := os.WriteFile(s.Paths().Memory, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() (Memory, error){s.Memory, s.EditableMemory} {
		m, err := read()
		if err != nil || m.Text != raw+"\n" || m.Warning != "" || m.Revision != memoryValue(raw).Revision {
			t.Fatalf("read %d bytes, warning %q, error %v", len(m.Text), m.Warning, err)
		}
	}
	if err := s.Remember("Extra"); !errors.Is(err, ErrMemorySize) {
		t.Fatalf("append: %v", err)
	}
	got, err := os.ReadFile(s.Paths().Memory)
	if err != nil || string(got) != raw {
		t.Fatalf("memory file changed: %v", err)
	}
	if err := os.WriteFile(s.Paths().Memory, []byte(raw+"x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Memory(); !errors.Is(err, ErrMemorySize) {
		t.Fatalf("oversized read: %v", err)
	}
}
