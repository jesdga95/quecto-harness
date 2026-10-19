package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

const maxReadableFile = 64 << 20

// sniffBytes is how much of a file is checked for binary content.
const sniffBytes = 8192

// readers holds the buffered readers read and grep open files with, so a
// read does not allocate a fresh 64 KB buffer.
var readers = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 64<<10) }}

func openReader(f *os.File) *bufio.Reader {
	rd := readers.Get().(*bufio.Reader)
	rd.Reset(f)
	return rd
}

func closeReader(rd *bufio.Reader) {
	rd.Reset(nil)
	readers.Put(rd)
}

// sniff reports whether the start of a buffered file looks binary, without
// consuming it.
func sniff(rd *bufio.Reader) (bool, error) {
	head, err := rd.Peek(sniffBytes)
	if err != nil && err != io.EOF {
		return false, err
	}
	return looksBinary(head), nil
}

// ReadFile returns numbered lines from a text file in the project.
func (r *Runner) ReadFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args ReadFileArgs
	if err := DecodeArgs("read", raw, &args); err != nil {
		return Result{}, err
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

	rd := openReader(f)
	defer closeReader(rd)
	binary, err := sniff(rd)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
	}
	if binary {
		return Result{}, fmt.Errorf("%s looks like a binary file, not read", args.Path)
	}

	offset := 1
	if args.Offset != nil && *args.Offset > 0 {
		offset = *args.Offset
	}
	limit := r.ReadLines
	if args.Limit != nil && *args.Limit > 0 {
		limit = *args.Limit
	}

	clip := min(r.ReadLimit, readLineClip)

	out := make([]byte, 0, min(int(info.Size())+int(info.Size())/4+64, r.ReadLimit+1024))
	var scratch []byte
	lineNo, emitted, stoppedOnBytes := 0, 0, false
	for emitted < limit {
		line, over, ok, err := readLine(rd, clip, &scratch)
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
		if len(out) >= r.ReadLimit {
			stoppedOnBytes = true
			break
		}
		out = appendLineNo(out, lineNo)
		out = append(out, '\t')
		out = append(out, line...)
		if over > 0 {
			out = fmt.Appendf(out, " ... %d more bytes on this line", over)
		}
		out = append(out, '\n')
		emitted++
	}

	truncated := false
	if stoppedOnBytes {
		out = fmt.Appendf(out, "... stopped at %d KB, use offset %d\n", r.ReadLimit/1024, lineNo)
		truncated = true
	} else {
		remaining, counted := 0, true
		for {
			_, _, ok, err := readLine(rd, 0, &scratch)
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
			out = fmt.Appendf(out, "... more lines follow, use offset %d\n", lineNo+1)
			truncated = true
		case remaining > 0:
			out = fmt.Appendf(out, "... %d more lines, use offset %d\n", remaining, lineNo+1)
			truncated = true
		}
	}
	if emitted == 0 && !stoppedOnBytes {
		return Result{Output: fmt.Sprintf("%s has no line %d, the file has %d lines", args.Path, offset, lineNo)}, nil
	}
	if r.Seen != nil {
		r.Seen.note(path, info)
	}
	return Result{Output: string(out), Truncated: truncated, Bytes: len(out)}, nil
}

// appendLineNo writes a line number right-aligned in six columns.
func appendLineNo(dst []byte, n int) []byte {
	var digits [20]byte
	d := strconv.AppendInt(digits[:0], int64(n), 10)
	for i := len(d); i < 6; i++ {
		dst = append(dst, ' ')
	}
	return append(dst, d...)
}

// readLineClip is how much of one line a read shows. A minified file is one
// line, and a clip this size leaves room in the page for the lines after it.
const readLineClip = 2000

// readLine returns the next line without its line ending, keeping at most clip
// bytes of it and counting the rest as over. A minified file is one very long
// line, and clipping it keeps the rest of the file readable where refusing the
// line would abandon the whole read. ok is false at end of file.
//
// The line is valid until the next call: it points into the reader's buffer,
// or into scratch when it spans more than one buffer or is clipped.
func readLine(rd *bufio.Reader, clip int, scratch *[]byte) (line []byte, over int, ok bool, err error) {
	buf := (*scratch)[:0]
	long, seen := false, false
	for {
		chunk, err := rd.ReadSlice('\n')
		if len(chunk) > 0 {
			seen = true
		}
		switch err {
		case bufio.ErrBufferFull:
			buf, over = appendClipped(buf, chunk, over, clip)
			long = true
		case nil, io.EOF:
			if err == io.EOF && !seen {
				return nil, 0, false, nil
			}
			if n := len(chunk); n > 0 && chunk[n-1] == '\n' {
				chunk = chunk[:n-1]
				if n := len(chunk); n > 0 && chunk[n-1] == '\r' {
					chunk = chunk[:n-1]
				}
			}
			if !long && len(chunk) <= clip {
				return chunk, 0, true, nil
			}
			buf, over = appendClipped(buf, chunk, over, clip)
			*scratch = buf
			return buf, over, true, nil
		default:
			return nil, 0, false, err
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
