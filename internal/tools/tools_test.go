package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"quecto-harness/internal/config"
)

// testRoot builds the layout the path guard table is written against.
func testRoot(t *testing.T, base string) *Runner {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "file.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "sub"), filepath.Join(base, "link_in")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(base, "link_out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(base, "sub", "link_out")); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.CommandTimeout = 5 * time.Second
	r, err := NewRunner(base, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPathGuard(t *testing.T) {
	// The /tmp copy only passes because the root is symlink-resolved at
	// construction; on macOS /tmp is itself a symlink.
	tmpBase, err := os.MkdirTemp("/tmp", "quecto-guard-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpBase)

	for _, base := range []string{t.TempDir(), tmpBase} {
		r := testRoot(t, base)
		cases := []struct {
			arg     string
			allowed bool
			says    string
		}{
			{"file.txt", true, ""},
			{"sub/new.txt", true, ""},
			{"sub/deep/new.txt", true, ""},
			{"./file.txt", true, ""},
			{"../outside.txt", false, "outside the project"},
			{"sub/../../outside.txt", false, "outside the project"},
			{"/etc/passwd", false, "outside the project"},
			{"link_in/x.txt", true, ""},
			{"link_out/passwd", false, "outside the project"},
			{"sub/link_out/passwd", false, "outside the project"},
			{"", false, "path is empty"},
			{"a\x00b", false, "null byte"},
		}
		for _, tc := range cases {
			got, err := r.resolve(tc.arg)
			if tc.allowed {
				if err != nil {
					t.Errorf("%s: resolve(%q) = %v, want allowed", base, tc.arg, err)
				} else if !strings.HasPrefix(got, r.Root) {
					t.Errorf("%s: resolve(%q) = %q, outside the root", base, tc.arg, got)
				}
				continue
			}
			if err == nil {
				t.Errorf("%s: resolve(%q) = %q, want refused", base, tc.arg, got)
				continue
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("%s: resolve(%q) said %q, want %q", base, tc.arg, err, tc.says)
			}
		}
	}
}

func TestRootItselfIsRefusedAsAFile(t *testing.T) {
	r := testRoot(t, t.TempDir())
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"."}`)); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("read_file on the root = %v, want a directory refusal", err)
	}
	if _, err := r.WriteFile(context.Background(), json.RawMessage(`{"path":".","content":"x"}`)); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("write_file on the root = %v, want a directory refusal", err)
	}
}

func TestReadFileNumbering(t *testing.T) {
	r := testRoot(t, t.TempDir())
	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"file.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "     1\tone\n     2\ttwo\n     3\tthree\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestReadFileOffsetAndLimit(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(filepath.Join(base, "big.txt"), []byte(b.String()), 0o644)

	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"big.txt","offset":10,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Output, "    10\tline\n") {
		t.Errorf("output starts %q, want line 10 first", res.Output[:20])
	}
	if !strings.Contains(res.Output, "... 88 more lines, use offset 13") {
		t.Errorf("output does not report the remainder: %q", res.Output)
	}
	if !res.Truncated {
		t.Error("truncated flag not set")
	}
}

func TestReadFileByteCap(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	r.ReadLimit = 200
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("a line of text\n")
	}
	os.WriteFile(filepath.Join(base, "wide.txt"), []byte(b.String()), 0o644)
	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"wide.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "stopped at") {
		t.Errorf("output does not report the byte cap: %q", res.Output)
	}
}

func TestReadFileEdgeCases(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	os.WriteFile(filepath.Join(base, "empty.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(base, "bin.dat"), []byte{'a', 0, 'b', 'c'}, 0o644)

	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"empty.txt"}`))
	if err != nil || !strings.Contains(res.Output, "is empty") {
		t.Errorf("empty file: %q %v", res.Output, err)
	}
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"bin.dat"}`)); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("binary file = %v", err)
	}
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"sub"}`)); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory = %v", err)
	}
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "needs a path") {
		t.Errorf("missing path = %v", err)
	}
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"file.txt","nonsense":1}`)); err != nil {
		t.Errorf("an invented field must not fail the call: %v", err)
	}
}

func TestWriteFileCreatesParentsAndPreservesMode(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	res, err := r.WriteFile(context.Background(), json.RawMessage(`{"path":"sub/deep/new.txt","content":"hello\nthere\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Output, "wrote sub/deep/new.txt (12 bytes, 2 lines)") {
		t.Errorf("output = %q", res.Output)
	}
	got, err := os.ReadFile(filepath.Join(base, "sub", "deep", "new.txt"))
	if err != nil || string(got) != "hello\nthere\n" {
		t.Errorf("file = %q %v", got, err)
	}

	target := filepath.Join(base, "exec.sh")
	os.WriteFile(target, []byte("old"), 0o755)
	res, err = r.WriteFile(context.Background(), json.RawMessage(`{"path":"exec.sh","content":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Output, "replaced exec.sh") {
		t.Errorf("output = %q, want the replaced wording", res.Output)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want the existing 0755 preserved", info.Mode().Perm())
	}
}

func TestWriteFileRefusesOutsideProject(t *testing.T) {
	r := testRoot(t, t.TempDir())
	if _, err := r.WriteFile(context.Background(), json.RawMessage(`{"path":"link_out/x","content":"x"}`)); err == nil {
		t.Fatal("a symlink out of the project must be refused")
	}
	if _, err := os.Stat("/etc/x"); err == nil {
		t.Fatal("the write escaped the project")
	}
}

func TestRunShellExitCodeAndMergedStreams(t *testing.T) {
	r := testRoot(t, t.TempDir())
	res, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"echo out; echo err 1>&2; exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("exit code = %v", res.ExitCode)
	}
	if !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") {
		t.Errorf("output = %q, both streams must be kept", res.Output)
	}
	if !strings.HasSuffix(res.Output, "exit code: 3") {
		t.Errorf("output = %q, want the exit code line last", res.Output)
	}
}

func TestRunShellRunsInProjectRoot(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	res, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, r.Root) {
		t.Errorf("output = %q, want the resolved root %q", res.Output, r.Root)
	}
}

func TestRunShellTimeoutKillsTheGroup(t *testing.T) {
	r := testRoot(t, t.TempDir())
	start := time.Now()
	res, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"sleep 30","timeout_s":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("took %v, the timeout did not kill the command", elapsed)
	}
	if !strings.Contains(res.Output, "timed out after 1s") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestRunShellForkedChildIsKilled(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	marker := filepath.Join(base, "marker")
	cmd := "sh -c 'sleep 3; echo late > " + marker + "' & sleep 30"
	if _, err := r.RunShell(context.Background(), json.RawMessage(`{"command":`+mustJSON(cmd)+`,"timeout_s":1}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a forked child outlived the process group kill")
	}
}

func TestRunShellStopped(t *testing.T) {
	r := testRoot(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	res, err := r.RunShell(ctx, json.RawMessage(`{"command":"sleep 30"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != nil {
		t.Errorf("exit code = %v, a stop reports none", *res.ExitCode)
	}
	if !strings.HasSuffix(res.Output, "stopped") {
		t.Errorf("output = %q", res.Output)
	}
}

// The clamp is what the command runs under and what the turn publishes before
// it starts, so the two cannot disagree about how long a call has.
func TestRunShellTimeoutIsClamped(t *testing.T) {
	r := testRoot(t, t.TempDir())
	if r.MaxTimeout != 600*time.Second {
		t.Errorf("MaxTimeout = %v, want the 600s floor", r.MaxTimeout)
	}
	cases := []struct {
		args string
		want time.Duration
	}{
		{`{"command":"true"}`, 5 * time.Second},
		{`{"command":"true","timeout_s":60}`, 60 * time.Second},
		{`{"command":"true","timeout_s":100000}`, 600 * time.Second},
		{`{"command":"true","timeout_s":0}`, time.Second},
		{`{"command":"true","timeout_s":-5}`, time.Second},
		{`not json`, 5 * time.Second},
	}
	for _, c := range cases {
		if got := r.ShellTimeout(json.RawMessage(c.args)); got != c.want {
			t.Errorf("ShellTimeout(%s) = %v, want %v", c.args, got, c.want)
		}
	}
	if _, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"true","timeout_s":100000}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRunShellNeedsACommand(t *testing.T) {
	r := testRoot(t, t.TempDir())
	if _, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"  "}`)); err == nil {
		t.Error("an empty command must be refused")
	}
}

func TestRunShellTruncation(t *testing.T) {
	r := testRoot(t, t.TempDir())
	r.OutputLimit = 1024
	res, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"for i in $(seq 1 500); do echo line $i; done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("truncated flag not set")
	}
	if !strings.Contains(res.Output, "bytes omitted") {
		t.Errorf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "line 1\n") || !strings.Contains(res.Output, "line 500") {
		t.Error("truncation must keep the head and the tail")
	}
}

func TestTruncateBoundaries(t *testing.T) {
	if got, tr := Truncate("short", 100); got != "short" || tr {
		t.Errorf("short input = %q %v", got, tr)
	}
	long := strings.Repeat("abcdefghij\n", 200)
	got, tr := Truncate(long, 300)
	if !tr {
		t.Fatal("long input not truncated")
	}
	if !strings.HasPrefix(got, "abcdefghij\n") || !strings.HasSuffix(got, "abcdefghij\n") {
		t.Errorf("cuts did not land on line boundaries: %q", got[:30])
	}
	unicodeText := strings.Repeat("こんにちは", 200)
	got, _ = Truncate(unicodeText, 300)
	if strings.Contains(got, "�") {
		t.Error("truncation split a rune")
	}
}

func TestLooksBinary(t *testing.T) {
	cases := []struct {
		in   []byte
		want bool
	}{
		{[]byte("plain text\n"), false},
		{[]byte("héllo wörld, still text"), false},
		{[]byte{'a', 0, 'b'}, true},
		{[]byte{0xff, 0xfe, 0xff, 0xfe}, true},
		{nil, false},
	}
	for _, tc := range cases {
		if got := looksBinary(tc.in); got != tc.want {
			t.Errorf("looksBinary(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSchemasMatchTheDescriptions(t *testing.T) {
	s := Schemas(Offer{Mode: config.WorkExecute, Ask: true, Agents: true, Message: true, Search: true})
	names := []string{"read_file", "find", "web_search", "write_file", "replace_in_file",
		"write_todos", "ask_question", "run_agent", "send_message", "run_shell"}
	if len(s) != len(names) {
		t.Fatalf("got %d schemas, want %d", len(s), len(names))
	}
	for i, want := range names {
		if s[i].Function.Name != want {
			t.Errorf("schema %d = %q, want %q", i, s[i].Function.Name, want)
		}
		if s[i].Function.Description == "" {
			t.Errorf("%s has no description", want)
		}
		var params map[string]any
		if err := json.Unmarshal(s[i].Function.Parameters, &params); err != nil {
			t.Errorf("%s parameters do not parse: %v", want, err)
		}
		if params["additionalProperties"] != false {
			t.Errorf("%s allows extra properties", want)
		}
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestReadAttachment(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(text, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAttachment(text)
	if err != nil {
		t.Fatalf("read a text file: %v", err)
	}
	if got.Kind != "text" || got.Content != "one\ntwo\n" {
		t.Errorf("attached = %+v", got)
	}

	binary := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binary, []byte{0x00, 0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAttachment(binary); err == nil {
		t.Error("a binary file was attached")
	}

	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), MaxAttachment+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAttachment(big); err == nil {
		t.Error("a file over the cap was attached")
	}

	if _, err := ReadAttachment("notes.md"); err == nil {
		t.Error("a relative path was attached")
	}
	if _, err := ReadAttachment(dir); err == nil {
		t.Error("a folder was attached")
	}

	// A png sniffs as an image, which is read as bytes rather than as text
	// and is left on disk for the file store to copy.
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	image, err := ReadAttachment(png)
	if err != nil {
		t.Fatalf("attach a png: %v", err)
	}
	if image.Kind != "image" || image.Mime != "image/png" || image.Content != "" {
		t.Errorf("attached = %+v", image)
	}
}

// onePixelPNG is the smallest file that sniffs as a png.
func onePixelPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89,
	}
}

func TestReplaceInFileChangesOneRun(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	target := filepath.Join(base, "hub.go")
	os.WriteFile(target, []byte("one\ntwo\nthree\n"), 0o640)

	var kept Snapshot
	r.Baseline = func(s Snapshot) { kept = s }

	res, err := r.ReplaceInFile(context.Background(),
		json.RawMessage(`{"path":"hub.go","old_string":"two","new_string":"TWO"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "edited hub.go at line 2 (-1 +1)" {
		t.Errorf("output = %q", res.Output)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "one\nTWO\nthree\n" {
		t.Errorf("file = %q", got)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 preserved", info.Mode().Perm())
	}
	if kept.Path != "hub.go" || string(kept.Content) != "one\ntwo\nthree\n" {
		t.Errorf("the baseline is %+v, want the file as it was before the edit", kept)
	}
}

// A match that is not unique is the case the tool exists to refuse: replacing
// the first of several is a silent wrong edit.
func TestReplaceInFileRefusesAmbiguityAndMisses(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	original := "x = 1\ny = 2\nx = 1\n"
	target := filepath.Join(base, "twice.txt")
	os.WriteFile(target, []byte(original), 0o644)

	_, err := r.ReplaceInFile(context.Background(),
		json.RawMessage(`{"path":"twice.txt","old_string":"x = 1","new_string":"x = 9"}`))
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Errorf("ambiguous match = %v", err)
	}
	_, err = r.ReplaceInFile(context.Background(),
		json.RawMessage(`{"path":"twice.txt","old_string":"z = 3","new_string":"z = 4"}`))
	if err == nil || !strings.Contains(err.Error(), "not in twice.txt") {
		t.Errorf("missing match = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != original {
		t.Errorf("a refused edit still wrote: %q", got)
	}
}

func TestReplaceInFileRefusesWhatItCannotEdit(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	os.WriteFile(filepath.Join(base, "blob.bin"), []byte("a\x00b\x00c"), 0o644)
	os.WriteFile(filepath.Join(base, "ok.txt"), []byte("keep\n"), 0o644)

	cases := []struct {
		name, args, want string
	}{
		{"missing file", `{"path":"gone.txt","old_string":"a","new_string":"b"}`, "no such file"},
		{"binary", `{"path":"blob.bin","old_string":"a","new_string":"b"}`, "binary file"},
		{"outside the project", `{"path":"../escape.txt","old_string":"a","new_string":"b"}`, "outside"},
		{"no old_string", `{"path":"ok.txt","new_string":"b"}`, "needs old_string"},
		{"nothing to change", `{"path":"ok.txt","old_string":"keep","new_string":"keep"}`, "already what you asked for"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := r.ReplaceInFile(context.Background(), json.RawMessage(c.args))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// Deleting a run of text is an edit with an empty new_string.
func TestReplaceInFileDeletes(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	target := filepath.Join(base, "list.txt")
	os.WriteFile(target, []byte("keep\ndrop\nkeep too\n"), 0o644)

	res, err := r.ReplaceInFile(context.Background(),
		json.RawMessage(`{"path":"list.txt","old_string":"drop\n","new_string":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "(-1 +0)") {
		t.Errorf("output = %q, want one line out and none in", res.Output)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep\nkeep too\n" {
		t.Errorf("file = %q", got)
	}
}

// TestLimitBufferRenderCounts pins the byte count the model is told. Cutting
// once in the buffer and again in Truncate used to report only the second cut,
// which named 114631 bytes for a command that had produced 100 MB.
func TestLimitBufferRenderCounts(t *testing.T) {
	kept := func(s string) int {
		i := strings.Index(s, "\n... ")
		j := strings.Index(s, " bytes omitted ...\n")
		if i < 0 || j < 0 {
			t.Fatalf("no marker in %q", s)
		}
		n := 0
		if _, err := fmt.Sscanf(s[i+5:j], "%d", &n); err != nil {
			t.Fatal(err)
		}
		return len(s) - (j + len(" bytes omitted ...\n") - i) + n
	}

	// Small enough to survive whole: byte-exact, and no marker.
	b := newLimitBuffer(4096)
	b.Write([]byte("hello\nworld\n"))
	if got, tr := b.Render(4096); got != "hello\nworld\n" || tr {
		t.Errorf("small output = %q %v", got, tr)
	}

	// Past the limit but inside the buffer's window, and far past it: the
	// omitted count plus what survived must be the true total either way.
	for _, lines := range []int{30, 100000} {
		b := newLimitBuffer(4096)
		for i := 0; i < lines; i++ {
			b.Write([]byte(strings.Repeat("y", 999) + "\n"))
		}
		out, tr := b.Render(4096)
		if !tr {
			t.Fatalf("%d lines: not truncated", lines)
		}
		if got := kept(out); got != b.Total() {
			t.Errorf("%d lines: marker accounts for %d bytes, total is %d", lines, got, b.Total())
		}
	}
}

// TestReadFileLongLine covers a minified file, which is one line longer than
// any scan buffer. It used to fail the whole read with "bufio.Scanner: token
// too long", and worse, a long line just past the page silently ended the
// output as though the file had ended there.
func TestReadFileLongLine(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	long := strings.Repeat("m", 3<<20)

	// A long line inside the page: the read survives it and says what it cut.
	body := strings.Repeat("short\n", 10) + long + "\nafter\n"
	if err := os.WriteFile(filepath.Join(base, "early.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"early.txt"}`))
	if err != nil {
		t.Fatalf("a long line must not fail the read: %v", err)
	}
	if !strings.Contains(res.Output, "more bytes on this line") {
		t.Errorf("the clip was not named: %q", tail(res.Output, 120))
	}
	if len(res.Output) > 4*r.ReadLimit {
		t.Errorf("output is %d bytes, the clip did not bound it", len(res.Output))
	}

	// A long line just past the page must still be counted as more to come.
	var page strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&page, "line %d\n", i)
	}
	page.WriteString(long + "\n" + strings.Repeat("tail\n", 50))
	if err := os.WriteFile(filepath.Join(base, "late.txt"), []byte(page.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = r.ReadFile(context.Background(), json.RawMessage(`{"path":"late.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || !strings.Contains(res.Output, "use offset 401") {
		t.Fatalf("the rest of the file was not announced: %q", tail(res.Output, 120))
	}
	res, err = r.ReadFile(context.Background(), json.RawMessage(`{"path":"late.txt","offset":402,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "   402\ttail") {
		t.Errorf("reading past the long line = %q", res.Output)
	}
}

// TestReadFileLineEndings keeps the reader on bufio.ScanLines behaviour.
func TestReadFileLineEndings(t *testing.T) {
	base := t.TempDir()
	r := testRoot(t, base)
	if err := os.WriteFile(filepath.Join(base, "crlf.txt"), []byte("one\r\ntwo\r\nthree"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"crlf.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "     1\tone\n     2\ttwo\n     3\tthree\n" {
		t.Errorf("output = %q", res.Output)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

func findTree(t *testing.T) *Runner {
	t.Helper()
	base := t.TempDir()
	files := map[string]string{
		"web/Chat.tsx":              "const line = 1\n<div class=\"chat-line\">\n",
		"web/Chat.css":              ".chat-line { color: red }\n",
		"web/Chat.module.css":       ".x {}\n",
		"web/other.tsx":             "nothing here\n",
		"node_modules/dep/index.js": "chat-line lives here too\n",
		"internal/thing.go":         "package thing\n\nfunc Chat() {}\n",
	}
	for name, body := range files {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRunner(base, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func find(t *testing.T, r *Runner, args string) string {
	t.Helper()
	res, err := r.Find(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("find %s: %v", args, err)
	}
	return res.Output
}

func TestFindTextIgnoresCaseAndIsLiteral(t *testing.T) {
	r := findTree(t)
	out := find(t, r, `{"text":"CHAT-LINE"}`)
	for _, want := range []string{"web/Chat.css:1:", "web/Chat.tsx:2:", "2 matches in 2 files"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	// A literal search takes the dot as a dot rather than as any character.
	if out := find(t, r, `{"text":"chat.line"}`); !strings.Contains(out, "no match for") {
		t.Errorf("literal search matched a regular expression:\n%s", out)
	}
	if out := find(t, r, `{"text":"chat.line","regex":true}`); !strings.Contains(out, "2 matches") {
		t.Errorf("regex search did not match:\n%s", out)
	}
}

func TestFindSkipsInstalledCodeUntilNamed(t *testing.T) {
	r := findTree(t)
	out := find(t, r, `{"text":"chat-line"}`)
	if strings.Contains(out, "node_modules") == false || !strings.Contains(out, "passed over") {
		t.Errorf("the skip was not reported:\n%s", out)
	}
	if strings.Contains(out, "node_modules/dep/index.js") {
		t.Errorf("node_modules was searched without being named:\n%s", out)
	}
	named := find(t, r, `{"text":"chat-line","path":"node_modules"}`)
	if !strings.Contains(named, "node_modules/dep/index.js:1:") {
		t.Errorf("naming the folder did not search it:\n%s", named)
	}
	if strings.Contains(named, "passed over") {
		t.Errorf("a named path still reported a skip:\n%s", named)
	}
}

func TestFindRelatedFilesAreSiblingsOfAMatch(t *testing.T) {
	r := findTree(t)
	out := find(t, r, `{"text":"const line"}`)
	if !strings.Contains(out, "related files:") {
		t.Fatalf("no related files for a search in one file:\n%s", out)
	}
	for _, want := range []string{"web/Chat.css", "web/Chat.module.css"} {
		if !strings.Contains(out, want) {
			t.Errorf("related files omit %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "web/other.tsx") {
		t.Errorf("a file with another name was called related:\n%s", out)
	}
}

func TestFindRelatedIsSuppressedWhenTheSearchIsBroad(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(base, name+".go"), []byte("hit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name+".css"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRunner(base, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if out := find(t, r, `{"text":"hit"}`); strings.Contains(out, "related files:") {
		t.Errorf("related files listed for a search in three files:\n%s", out)
	}
}

func TestFindByNameListsPaths(t *testing.T) {
	r := findTree(t)
	out := find(t, r, `{"name":"*.css"}`)
	if !strings.Contains(out, "web/Chat.css") || !strings.Contains(out, "2 files") {
		t.Errorf("name search did not list the stylesheets:\n%s", out)
	}
	if strings.Contains(out, ":1:") {
		t.Errorf("name search returned lines:\n%s", out)
	}
	if out := find(t, r, `{"name":"*.rs"}`); !strings.Contains(out, "no file named *.rs") {
		t.Errorf("a name that matches nothing did not say so:\n%s", out)
	}
}

func TestFindNameAndTextTogetherNarrow(t *testing.T) {
	r := findTree(t)
	out := find(t, r, `{"text":"chat-line","name":"*.css"}`)
	if !strings.Contains(out, "web/Chat.css:1:") || !strings.Contains(out, "1 match in 1 file") {
		t.Errorf("the name did not narrow the text search:\n%s", out)
	}
	if strings.Contains(out, "web/Chat.tsx:2:") {
		t.Errorf("a file outside the name pattern was searched:\n%s", out)
	}
}

func TestFindBadInput(t *testing.T) {
	r := findTree(t)
	if _, err := r.Find(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("find with no name and no text was accepted")
	}
	if _, err := r.Find(context.Background(), json.RawMessage(`{"text":"x","path":"../outside"}`)); err == nil {
		t.Error("find escaped the project")
	}
	if _, err := r.Find(context.Background(), json.RawMessage(`{"text":"x","path":"nope"}`)); err == nil {
		t.Error("find accepted a path that does not exist")
	}
	out := find(t, r, `{"text":"(unclosed","regex":true}`)
	if !strings.Contains(out, "not a valid regular expression") || !strings.Contains(out, "without regex") {
		t.Errorf("a bad expression did not say how to retry:\n%s", out)
	}
}

func TestFindClipsAndCaps(t *testing.T) {
	base := t.TempDir()
	long := "needle " + strings.Repeat("x", 900)
	var many strings.Builder
	for i := 0; i < 50; i++ {
		many.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(base, "long.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "many.txt"), []byte(many.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "bin.dat"), []byte("needle\x00needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(base, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	out := find(t, r, `{"text":"needle"}`)
	for _, line := range strings.Split(out, "\n") {
		if len(line) > findLineClip+64 {
			t.Errorf("a result line was not clipped: %d bytes", len(line))
		}
	}
	if strings.Count(out, "many.txt:") > findMaxPerFile+1 {
		t.Errorf("the per-file cap did not hold:\n%s", out)
	}
	if strings.Contains(out, "bin.dat") {
		t.Errorf("a binary file was searched:\n%s", out)
	}
}

// A turn is sent the tools it offers and no others, so a session that cannot
// run an agent is never handed the schema for it.
func TestSchemasFollowTheOffer(t *testing.T) {
	names := func(o Offer) []string {
		var out []string
		for _, s := range Schemas(o) {
			out = append(out, s.Function.Name)
		}
		return out
	}
	base := []string{"read_file", "find", "write_file", "replace_in_file", "write_todos", "run_shell"}
	optional := map[string]Offer{
		"ask_question": {Mode: config.WorkExecute, Ask: true},
		"run_agent":    {Mode: config.WorkExecute, Agents: true},
		"send_message": {Mode: config.WorkExecute, Message: true},
		"web_search":   {Mode: config.WorkExecute, Search: true},
	}

	none := names(Offer{Mode: config.WorkExecute})
	if len(none) != len(base) {
		t.Errorf("an empty offer sent %v, want %v", none, base)
	}
	for _, want := range base {
		if !slices.Contains(none, want) {
			t.Errorf("an empty offer left out %s, which every session has", want)
		}
	}
	for name, only := range optional {
		got := names(only)
		if !slices.Contains(got, name) {
			t.Errorf("%s was offered and not sent: %v", name, got)
		}
		if len(got) != len(base)+1 {
			t.Errorf("offering %s sent %v", name, got)
		}
	}

	all := names(Offer{Mode: config.WorkExecute, Ask: true, Agents: true, Message: true, Search: true})
	if len(all) != len(base)+len(optional) {
		t.Errorf("every offer sent %v", all)
	}
	// The order the model sees is the order they are declared, whichever are
	// left out.
	if !slices.IsSorted([]int{indexOf(all, "read_file"), indexOf(all, "run_shell")}) {
		t.Errorf("the order moved: %v", all)
	}
}

func indexOf(in []string, want string) int {
	for i, v := range in {
		if v == want {
			return i
		}
	}
	return -1
}

// Plan mode sends no tool that writes a file, and is the only mode that sends
// submit_plan. A tool the offer leaves out is still in its mode, so a call to
// it is refused by the tool rather than by the mode.
func TestSchemasFollowTheMode(t *testing.T) {
	every := Offer{Ask: true, Agents: true, Message: true, Search: true}
	plan, exec := every, every
	plan.Mode, exec.Mode = config.WorkPlan, config.WorkExecute

	got := Names(plan)
	want := []string{"read_file", "find", "web_search", "ask_question", "write_plan", "submit_plan",
		"run_agent", "send_message", "run_shell"}
	if !slices.Equal(got, want) {
		t.Errorf("plan sent %v, want %v", got, want)
	}
	for _, name := range []string{"write_file", "replace_in_file", "write_todos"} {
		if plan.InMode(name) {
			t.Errorf("plan mode holds %s", name)
		}
	}
	for _, name := range []string{"write_plan", "submit_plan"} {
		if slices.Contains(Names(exec), name) {
			t.Errorf("execute mode sent %s", name)
		}
	}
	if !(Offer{Mode: config.WorkExecute}).InMode("run_agent") {
		t.Error("run_agent left the mode when the offer left it out")
	}
	if len(Names(Offer{})) != 0 {
		t.Error("an offer with no mode sent tools")
	}
}
