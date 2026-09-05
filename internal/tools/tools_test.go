package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestRunShellTimeoutIsClamped(t *testing.T) {
	r := testRoot(t, t.TempDir())
	if r.MaxTimeout != 600*time.Second {
		t.Errorf("MaxTimeout = %v, want the 600s floor", r.MaxTimeout)
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
	s := Schemas()
	names := []string{"read_file", "write_file", "replace_in_file", "write_todos", "run_shell"}
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
