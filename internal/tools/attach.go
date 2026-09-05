package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Caps on what one message may carry. Text is inlined into the prompt, so it
// is held small; an image is sent as data and the model server resizes it.
const (
	MaxAttachment = 256 << 10
	MaxImage      = 6 << 20
)

// Attached is one file the user picked, as far as reading it goes. Content is
// filled for text; an image is left on disk for the caller to store.
type Attached struct {
	Kind    string
	Mime    string
	Content string
	Bytes   int
}

// imageTypes are the formats worth sending to a vision model. Anything else
// sniffs as something the model cannot read.
var imageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// ReadAttachment reads a file the user attached to a message. It is not held
// to a project root: the file was chosen in the host's own dialog, which is
// the user saying which file it is.
func ReadAttachment(path string) (Attached, error) {
	if !filepath.IsAbs(path) {
		return Attached{}, fmt.Errorf("attach %s: the path is not absolute", path)
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil {
		return Attached{}, fmt.Errorf("attach %s: no such file, or it cannot be read", path)
	}
	if info.IsDir() {
		return Attached{}, fmt.Errorf("attach %s: that is a folder", path)
	}
	if info.Size() == 0 {
		return Attached{}, fmt.Errorf("attach %s: the file is empty", path)
	}

	f, err := os.Open(clean)
	if err != nil {
		return Attached{}, fmt.Errorf("attach %s: check the permissions on the file", path)
	}
	defer f.Close()

	sniff := make([]byte, 512)
	n, err := f.Read(sniff)
	if err != nil && n == 0 {
		return Attached{}, fmt.Errorf("attach %s: %s", path, err)
	}
	mime := strings.SplitN(http.DetectContentType(sniff[:n]), ";", 2)[0]

	if imageTypes[mime] {
		if info.Size() > MaxImage {
			return Attached{}, fmt.Errorf("attach %s: the image is %d mb, the limit is %d mb",
				path, info.Size()/(1<<20), MaxImage/(1<<20))
		}
		return Attached{Kind: "image", Mime: mime, Bytes: int(info.Size())}, nil
	}

	if info.Size() > MaxAttachment {
		return Attached{}, fmt.Errorf("attach %s: the file is %d kb, the limit is %d kb",
			path, info.Size()/1024, MaxAttachment/1024)
	}
	if looksBinary(sniff[:n]) {
		return Attached{}, fmt.Errorf("attach %s: that is neither text nor an image the model can read", path)
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return Attached{}, fmt.Errorf("attach %s: check the permissions on the file", path)
	}
	return Attached{Kind: "text", Mime: "text/plain", Content: string(raw), Bytes: len(raw)}, nil
}
