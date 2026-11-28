package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryEditorConflict(t *testing.T) {
	f := newFixture(t)
	read := decode(t, f.do(t, "GET", "/api/memory", nil))
	if err := f.cfg.Remember("Keep this"); err != nil {
		t.Fatal(err)
	}
	read["text"] = "Stale edit"
	if w := f.do(t, "PUT", "/api/memory", read); w.Code != 409 {
		t.Fatalf("stale save: %d %s", w.Code, w.Body)
	}
	read = decode(t, f.do(t, "GET", "/api/memory", nil))
	if read["text"] != "Keep this\n" {
		t.Fatal(read)
	}
	read["text"] = "Next\r\n"
	saved := decode(t, f.do(t, "PUT", "/api/memory", read))
	if saved["text"] != "Next\n" || saved["revision"] == read["revision"] {
		t.Fatal(saved)
	}
	if w := f.do(t, "PUT", "/api/memory", map[string]any{"revision": saved["revision"]}); w.Code != 400 {
		t.Fatalf("missing text: %d", w.Code)
	}
}

func TestMemoryEditorRepairsDamage(t *testing.T) {
	for _, kind := range []string{"oversize", "encoding", "link"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			path := f.cfg.Paths().Memory
			target := filepath.Join(t.TempDir(), "target")
			src := "Readable\r\n" + strings.Repeat("x", 10000)
			switch kind {
			case "encoding":
				src = "Readable\r\n\xff"
			case "link":
				if err := os.WriteFile(target, []byte("Private"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "link" {
				if err := os.WriteFile(path, []byte(src), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.cfg.Remember("More"); err == nil {
				t.Fatal("appended to damaged memory")
			}
			get := func() map[string]any {
				t.Helper()
				w := f.do(t, "GET", "/api/memory", nil)
				if w.Code != 200 {
					t.Fatal(w.Body)
				}
				return decode(t, w)
			}
			m := get()
			text := m["text"].(string)
			if m["warning"] == nil || m["revision"] == "" {
				t.Fatal(m)
			}
			if kind == "link" && text != "" || kind != "link" && !strings.HasPrefix(text, "Readable\n") {
				t.Fatalf("preview: %.20q", text)
			}
			if kind == "oversize" {
				if len(text) >= len(src) {
					t.Fatal("unbounded preview")
				}
				if err := os.WriteFile(path, []byte(src[:len(src)-1]+"y"), 0600); err != nil {
					t.Fatal(err)
				}
				m["text"] = "Fixed"
				if w := f.do(t, "PUT", "/api/memory", m); w.Code != 409 {
					t.Fatal(w.Body)
				}
				m = get()
			}
			m["text"] = "Fixed"
			if w := f.do(t, "PUT", "/api/memory", m); w.Code != 200 {
				t.Fatal(w.Body)
			}
			if m = get(); m["warning"] != nil || m["text"] != "Fixed\n" {
				t.Fatal(m)
			}
			if kind == "link" {
				if b, err := os.ReadFile(target); err != nil || string(b) != "Private" {
					t.Fatal("link target changed")
				}
			}
		})
	}
}
