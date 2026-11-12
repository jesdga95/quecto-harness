package tools

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Caps on what one message may carry. Text is inlined into the prompt, so it
// is held small; an image is sent as data and the model server resizes it. A
// document and a video are sent whole on every turn.
const (
	MaxAttachment = 256 << 10
	MaxImage      = 6 << 20
	MaxDocument   = 64 << 20
	MaxVideo      = 256 << 20
)

// Attached is one file the user picked, as far as reading it goes. Content is
// filled for text; an image, a document or a video is left on disk for the
// caller to store. Kind is text, image, document or video.
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

// LooksLikeImage reports whether bytes sniff as an image a vision model can
// read, which is what decides the size an attachment is held to.
func LooksLikeImage(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return imageTypes[strings.SplitN(http.DetectContentType(head), ";", 2)[0]]
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

	if kind, mime := media(clean, sniff[:n]); kind != "" {
		limit := MaxDocument
		if kind == "video" {
			limit = MaxVideo
		}
		if info.Size() > int64(limit) {
			return Attached{}, fmt.Errorf("attach %s: the file is %d mb, the limit is %d mb",
				path, info.Size()/(1<<20), limit/(1<<20))
		}
		return Attached{Kind: kind, Mime: mime, Bytes: int(info.Size())}, nil
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

// documentTypes and videoTypes name the formats sent to the model server as
// they are, by extension. The bytes must also open the way the format does.
var (
	documentTypes = map[string]string{
		".pdf":  "application/pdf",
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}
	videoTypes = map[string]string{
		".mp4":  "video/mp4",
		".m4v":  "video/mp4",
		".mov":  "video/quicktime",
		".mkv":  "video/x-matroska",
		".webm": "video/webm",
	}
)

// isoBoxes are the boxes an MP4 or QuickTime file opens with. Older QuickTime
// files put the movie or its data ahead of the type box.
var isoBoxes = map[string]bool{"ftyp": true, "moov": true, "mdat": true, "wide": true, "free": true, "skip": true}

// media reports a document or a video, and the type it is sent as. Empty is
// neither. The extension names the format and the first bytes confirm it: a
// PDF header, a zip archive for an Office file, an ISO media box or a
// Matroska header for a video.
func media(path string, head []byte) (string, string) {
	ext := strings.ToLower(filepath.Ext(path))
	if mime, ok := documentTypes[ext]; ok {
		if ext == ".pdf" && bytes.HasPrefix(head, []byte("%PDF-")) ||
			ext != ".pdf" && bytes.HasPrefix(head, []byte("PK\x03\x04")) {
			return "document", mime
		}
	}
	if mime, ok := videoTypes[ext]; ok {
		isoMedia := len(head) >= 8 && isoBoxes[string(head[4:8])]
		matroska := bytes.HasPrefix(head, []byte{0x1a, 0x45, 0xdf, 0xa3})
		if isoMedia || matroska {
			return "video", mime
		}
	}
	return "", ""
}
