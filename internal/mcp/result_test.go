package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	r := Render(CallResult{Content: []Content{
		{Type: "text", Text: "A picture."},
		{Type: "image", MimeType: "image/png", Data: tinyPNG},
		{Type: "image", MimeType: "image/tiff", Data: tinyPNG},
		{Type: "image", MimeType: "image/png", Data: "%%%"},
		{Type: "resource_link", URI: "file:///a.txt", Name: "a.txt", MimeType: "text/plain"},
		{Type: "resource", Resource: &ResourceContents{URI: "file:///b.md", Text: "# B"}},
		{Type: "resource", Resource: &ResourceContents{URI: "file:///c.bin", Blob: "AAAA"}},
		{Type: "audio", MimeType: "audio/wav"},
		{Type: "video"},
	}, Structured: json.RawMessage(`{"ignored":true}`)})
	want := strings.Join([]string{
		"A picture.",
		"[image, image/tiff, not shown: the model takes PNG, JPEG, GIF or WebP]",
		"[image, image/png, not shown: the data is not valid base64]",
		"[resource link: a.txt, file:///a.txt, text/plain]",
		"[resource: file:///b.md]\n# B",
		"[resource: file:///c.bin, binary, not shown]",
		"[audio, audio/wav, not shown]",
		"[video content, not shown]",
	}, "\n\n")
	if r.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", r.Text, want)
	}
	if len(r.Images) != 1 || r.Images[0].Mime != "image/png" || !strings.HasPrefix(string(r.Images[0].Data), "\x89PNG") {
		t.Fatalf("images = %+v", r.Images)
	}
}

func TestRenderImageCap(t *testing.T) {
	var blocks []Content
	for range MaxImages + 2 {
		blocks = append(blocks, Content{Type: "image", MimeType: "image/png", Data: tinyPNG})
	}
	r := Render(CallResult{Content: blocks})
	if len(r.Images) != MaxImages || strings.Count(r.Text, "at most") != 2 {
		t.Fatalf("images %d, text %q", len(r.Images), r.Text)
	}
}

func TestRenderStructuredAndEmpty(t *testing.T) {
	r := Render(CallResult{Structured: json.RawMessage(`{"temp":21}`), IsError: true})
	if r.Text != "{\n  \"temp\": 21\n}" || !r.IsError {
		t.Fatalf("got %+v", r)
	}
	if r := Render(CallResult{}); r.Text != "(the tool returned nothing)" {
		t.Fatalf("empty = %q", r.Text)
	}
}

func TestWireName(t *testing.T) {
	cases := []struct {
		server, tool, want string
		ok                 bool
	}{
		{"figma", "get_code", "mcp__figma__get_code", true},
		{"gh", "list.issues v2", "mcp__gh__list_issues_v2", true},
		{"x", strings.Repeat("a", 60), "", false},
		{"", "a", "", false},
		{"x", "", "", false},
	}
	for _, c := range cases {
		got, ok := WireName(c.server, c.tool)
		if got != c.want || ok != c.ok {
			t.Errorf("WireName(%q, %q) = %q, %v", c.server, c.tool, got, ok)
		}
	}
}
