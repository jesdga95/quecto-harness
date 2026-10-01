package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type ReadImageArgs struct {
	Path string `json:"path"`
}

// Image is a picture a result carries beside its text. Abs is where it was
// found; the caller copies it from there before the next request.
type Image struct {
	Abs   string
	Path  string
	Mime  string
	Bytes int
}

// imageNames is what the result calls each format.
var imageNames = map[string]string{
	"image/png": "PNG", "image/jpeg": "JPEG", "image/gif": "GIF", "image/webp": "WebP",
}

// ReadImage checks that a path in the project names an image a vision model
// can read and returns it as the result's image. The type is sniffed from the
// bytes, so a name ending in .png proves nothing.
func (r *Runner) ReadImage(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args ReadImageArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("read_image: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("read_image needs a path")
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
	if info.Size() == 0 {
		return Result{}, fmt.Errorf("%s is empty", args.Path)
	}
	if info.Size() > MaxImage {
		return Result{}, fmt.Errorf("%s is %d MB, the limit is %d MB", args.Path, info.Size()/(1<<20), MaxImage/(1<<20))
	}

	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: check the permissions on the file", args.Path)
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Result{}, fmt.Errorf("read %s: %s", args.Path, err)
	}
	mime := strings.SplitN(http.DetectContentType(head[:n]), ";", 2)[0]
	if !imageTypes[mime] {
		return Result{}, fmt.Errorf("%s is not a PNG, JPEG, GIF or WebP image: use read for a text file", args.Path)
	}

	rel := r.rel(path)
	size := int(info.Size())
	return Result{
		Output: fmt.Sprintf("%s, %s, %s.", rel, imageNames[mime], sizeLabel(size)),
		Bytes:  size,
		Image:  &Image{Abs: path, Path: rel, Mime: mime, Bytes: size},
	}, nil
}

// sizeLabel is a file size in the unit that keeps it short.
func sizeLabel(n int) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
