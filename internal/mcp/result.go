package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxImages is how many pictures one result passes to the model. The rest
// are named in the text.
const MaxImages = 4

// imageTypes are the pictures a model request can carry.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// Image is one decoded picture from a result.
type Image struct {
	Mime string
	Data []byte
}

// Rendered is a result as the model reads it: text, the pictures that go
// with it, and whether the tool said it failed.
type Rendered struct {
	Text    string
	Images  []Image
	IsError bool
}

// Render turns a result into text and pictures. Structured content is shown
// only when no block carries text, since a server that sends both puts the
// same data in each.
func Render(r CallResult) Rendered {
	out := Rendered{IsError: r.IsError}
	var parts []string
	hasText := false
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			hasText = true
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, out.addImage(b.MimeType, b.Data)...)
		case "audio":
			parts = append(parts, fmt.Sprintf("[audio, %s, not shown]", orUnknown(b.MimeType)))
		case "resource_link":
			parts = append(parts, "[resource link: "+join(b.Name, b.URI, b.MimeType)+"]")
		case "resource":
			if b.Resource == nil {
				continue
			}
			if b.Resource.Text != "" {
				hasText = true
				parts = append(parts, "[resource: "+join(b.Resource.URI, b.Resource.MimeType)+"]\n"+b.Resource.Text)
			} else {
				parts = append(parts, "[resource: "+join(b.Resource.URI, b.Resource.MimeType)+", binary, not shown]")
			}
		default:
			parts = append(parts, fmt.Sprintf("[%s content, not shown]", b.Type))
		}
	}
	if !hasText && len(r.Structured) > 0 {
		var buf bytes.Buffer
		if json.Indent(&buf, r.Structured, "", "  ") == nil {
			parts = append(parts, buf.String())
		}
	}
	out.Text = strings.Join(parts, "\n\n")
	if strings.TrimSpace(out.Text) == "" && len(out.Images) == 0 {
		out.Text = "(the tool returned nothing)"
	}
	return out
}

// addImage keeps a picture the model can take, up to MaxImages, and returns
// the line that stands in for one it does not keep.
func (r *Rendered) addImage(mime, data string) []string {
	if !imageTypes[mime] {
		return []string{fmt.Sprintf("[image, %s, not shown: the model takes PNG, JPEG, GIF or WebP]", orUnknown(mime))}
	}
	if len(r.Images) == MaxImages {
		return []string{fmt.Sprintf("[image, %s, not shown: a result carries at most %d]", mime, MaxImages)}
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) == 0 {
		return []string{fmt.Sprintf("[image, %s, not shown: the data is not valid base64]", mime)}
	}
	r.Images = append(r.Images, Image{Mime: mime, Data: raw})
	return nil
}

func join(fields ...string) string {
	var out []string
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, ", ")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}
