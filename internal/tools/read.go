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
		return Result{}, fmt.Errorf("read: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("read needs a path")
	}
	path, err := r.resolveRead(args.Path)
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
		return Result{}, fmt.Errorf("%s is %d MB, too large to read: use bash with sed -n to take a range of lines", args.Path, info.Size()/(1<<20))
	}
	if info.Size() == 0 {
		if r.Seen != nil {
			r.Seen.note(path, info)
		}
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

	rd := bufio.NewReaderSize(f, 64<<10)
	clip := min(r.ReadLimit, readLineClip)

	var b strings.Builder
	lineNo, emitted, stoppedOnBytes := 0, 0, false
	for emitted < limit {
		line, over, ok, err := readLine(rd, clip)
		if err != nil {
			return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
		}
		if !ok {
			break
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		if over > 0 {
			line += fmt.Sprintf(" ... %d more bytes on this line", over)
		}
		if b.Len() >= r.ReadLimit {
			stoppedOnBytes = true
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, line)
		emitted++
	}

	out := b.String()
	truncated := false
	if stoppedOnBytes {
		fmt.Fprintf(&b, "... stopped at %d KB, use offset %d\n", r.ReadLimit/1024, lineNo)
		out = b.String()
		truncated = true
	} else {
		remaining, counted := 0, true
		for {
			_, _, ok, err := readLine(rd, 0)
			if err != nil {
				// Say that the file goes on rather than let a failed count
				// read as end of file, which would hide the rest of it.
				counted = false
				break
			}
			if !ok {
				break
			}
			remaining++
		}
		switch {
		case !counted:
			fmt.Fprintf(&b, "... more lines follow, use offset %d\n", lineNo+1)
			out = b.String()
			truncated = true
		case remaining > 0:
			fmt.Fprintf(&b, "... %d more lines, use offset %d\n", remaining, lineNo+1)
			out = b.String()
			truncated = true
		}
	}
	if emitted == 0 && !stoppedOnBytes {
		return Result{Output: fmt.Sprintf("%s has no line %d, the file has %d lines", args.Path, offset, lineNo)}, nil
	}
	if r.Seen != nil {
		r.Seen.note(path, info)
	}
	return Result{Output: out, Truncated: truncated, Bytes: len(out)}, nil
}

// readLineClip is how much of one line a read shows. A minified file is one
// line, and a clip this size leaves room in the page for the lines after it.
const readLineClip = 2000

// readLine returns the next line without its line ending, keeping at most clip
// bytes of it and counting the rest as over. A minified file is one very long
// line, and clipping it keeps the rest of the file readable where refusing the
// line would abandon the whole read. ok is false at end of file.
func readLine(rd *bufio.Reader, clip int) (line string, over int, ok bool, err error) {
	var buf []byte
	seen := false
	for {
		chunk, err := rd.ReadSlice('\n')
		if len(chunk) > 0 {
			seen = true
		}
		switch err {
		case bufio.ErrBufferFull:
			buf, over = appendClipped(buf, chunk, over, clip)
		case nil, io.EOF:
			if err == io.EOF && !seen {
				return "", 0, false, nil
			}
			if n := len(chunk); n > 0 && chunk[n-1] == '\n' {
				chunk = chunk[:n-1]
				if n := len(chunk); n > 0 && chunk[n-1] == '\r' {
					chunk = chunk[:n-1]
				}
			}
			buf, over = appendClipped(buf, chunk, over, clip)
			return string(buf), over, true, nil
		default:
			return "", 0, false, err
		}
	}
}

// appendClipped copies as much of chunk as the clip allows and counts the rest.
func appendClipped(buf, chunk []byte, over, clip int) ([]byte, int) {
	if room := clip - len(buf); room > 0 {
		if room > len(chunk) {
			room = len(chunk)
		}
		buf = append(buf, chunk[:room]...)
		chunk = chunk[room:]
	}
	return buf, over + len(chunk)
}
