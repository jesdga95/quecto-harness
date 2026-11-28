package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const memoryLimit = 4096

var (
	ErrMemoryChanged = errors.New("Memory changed since you opened it. Reload before saving.")
	ErrMemorySize    = errors.New("Memory exceeds 4096 bytes.")
)

type Memory struct {
	Text     string `json:"text"`
	Revision string `json:"revision"`
	Warning  string `json:"warning,omitempty"`
}

func memoryValue(text string) Memory {
	return Memory{Text: text, Revision: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))}
}

func (s *Store) Memory() (Memory, error) {
	return s.readMemory(false)
}

// EditableMemory returns the readable part and a revision for repairing a damaged file.
func (s *Store) EditableMemory() (Memory, error) { return s.readMemory(true) }

func (s *Store) readMemory(edit bool) (Memory, error) {
	f, err := os.OpenFile(s.paths.Memory, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return memoryValue(""), nil
	}
	if errors.Is(err, syscall.ELOOP) {
		if !edit {
			return Memory{}, errors.New("Memory is a symbolic link.")
		}
		target, err := os.Readlink(s.paths.Memory)
		if err != nil {
			return Memory{}, fmt.Errorf("Read memory link: %w", err)
		}
		return Memory{Revision: "link-" + memoryValue(target).Revision, Warning: "Memory is a symbolic link. Save in Settings to replace the link."}, nil
	}
	if err != nil {
		return Memory{}, fmt.Errorf("Read memory: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Memory{}, fmt.Errorf("Read memory: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Memory{}, errors.New("Memory must be a regular file.")
	}
	// CRLF can take two bytes per stored newline. The editor bounds the
	// preview and hashes the remainder to detect edits outside that preview.
	const preview = 2 * memoryLimit
	hash := sha256.New()
	b, err := io.ReadAll(io.TeeReader(io.LimitReader(f, preview+1), hash))
	if err != nil {
		return Memory{}, fmt.Errorf("Read memory: %w", err)
	}
	var m Memory
	if len(b) > preview {
		if !edit {
			return Memory{}, ErrMemorySize
		}
		if _, err := io.Copy(hash, f); err != nil {
			return Memory{}, fmt.Errorf("Read memory: %w", err)
		}
		m.Warning = ErrMemorySize.Error() + " Only the beginning is shown. Save to replace the entire file."
		b = b[:preview]
	}
	m.Revision = fmt.Sprintf("%x", hash.Sum(nil))
	m.Text = strings.ReplaceAll(string(b), "\r\n", "\n")
	if m.Warning == "" && len(m.Text) > memoryLimit {
		if !edit {
			return Memory{}, ErrMemorySize
		}
		m.Warning = ErrMemorySize.Error() + " Shorten it in Settings."
	}
	if !utf8.ValidString(m.Text) {
		if !edit {
			return Memory{}, errors.New("Memory contains invalid UTF-8.")
		}
		m.Text = strings.ToValidUTF8(m.Text, "�")
		if m.Warning == "" {
			m.Warning = "Memory contains invalid UTF-8. Save in Settings to repair the text."
		}
	}
	m.Text = endLine(m.Text)
	return m, nil
}

func (s *Store) ReplaceMemory(text, revision string) (Memory, error) {
	return s.writeMemory(text, &revision)
}

func (s *Store) Remember(text string) error {
	if strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return errors.New("Remove control and formatting characters from the note.")
	}
	if strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\u2028\u2029") {
		return errors.New("Provide one nonempty line to remember.")
	}
	_, err := s.writeMemory(strings.TrimSpace(text), nil)
	return err
}

// The directory lock serialises read-modify-write across processes. The
// memory file itself changes inode at each atomic replacement.
func (s *Store) writeMemory(text string, revision *string) (Memory, error) {
	dir, err := os.Open(s.paths.Home)
	if err != nil {
		return Memory{}, fmt.Errorf("Open state directory: %w", err)
	}
	defer dir.Close()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		return Memory{}, fmt.Errorf("Lock memory: %w", err)
	}
	current, err := s.readMemory(revision != nil)
	if err != nil {
		return Memory{}, err
	}
	if revision == nil {
		text = current.Text + text
	} else if current.Revision != *revision {
		return Memory{}, ErrMemoryChanged
	}
	text = endLine(strings.ReplaceAll(text, "\r\n", "\n"))
	if len(text) > memoryLimit {
		return Memory{}, ErrMemorySize
	}
	if err := WriteAtomic(s.paths.Memory, []byte(text), 0o600); err != nil {
		return Memory{}, err
	}
	return memoryValue(text), nil
}

func endLine(text string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		return text + "\n"
	}
	return text
}
