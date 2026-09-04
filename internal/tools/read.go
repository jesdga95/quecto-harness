package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxReadableFile = 64 << 20

// ReadFile returns numbered lines from a text file in the project.
func (r *Runner) ReadFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args ReadFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("read_file: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("read_file needs a path")
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("%s: no such file", args.Path)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory", args.Path)
	}
	if info.Size() > maxReadableFile {
		return Result{}, fmt.Errorf("%s is %d MB, too large to read", args.Path, info.Size()/(1<<20))
	}
	if info.Size() == 0 {
		return Result{Output: fmt.Sprintf("%s is empty", args.Path)}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: check the permissions on the file", args.Path)
	}
	defer f.Close()

	sniff := make([]byte, 8192)
	n, err := io.ReadFull(f, sniff)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
	}
	if looksBinary(sniff[:n]) {
		return Result{}, fmt.Errorf("%s looks like a binary file, not read", args.Path)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
	}

	offset := 1
	if args.Offset != nil && *args.Offset > 0 {
		offset = *args.Offset
	}
	limit := r.ReadLines
	if args.Limit != nil && *args.Limit > 0 {
		limit = *args.Limit
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var b strings.Builder
	lineNo, emitted, stoppedOnBytes := 0, 0, false
	for emitted < limit {
		if !sc.Scan() {
			break
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		line := sc.Text()
		if len(line) >= 1<<20 {
			line = line[:1<<20] + " ..."
		}
		if b.Len() >= r.ReadLimit {
			stoppedOnBytes = true
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, line)
		emitted++
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
	}

	out := b.String()
	truncated := false
	if stoppedOnBytes {
		fmt.Fprintf(&b, "... stopped at %d KB, use offset %d\n", r.ReadLimit/1024, lineNo)
		out = b.String()
		truncated = true
	} else {
		remaining := 0
		for sc.Scan() {
			remaining++
		}
		if remaining > 0 {
			fmt.Fprintf(&b, "... %d more lines, use offset %d\n", remaining, lineNo+1)
			out = b.String()
			truncated = true
		}
	}
	if emitted == 0 && !stoppedOnBytes {
		return Result{Output: fmt.Sprintf("%s has no line %d, the file has %d lines", args.Path, offset, lineNo)}, nil
	}
	return Result{Output: out, Truncated: truncated, Bytes: len(out)}, nil
}
