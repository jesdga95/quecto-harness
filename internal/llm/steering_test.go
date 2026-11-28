package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSteerOnTheWire(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolChat, ProtocolResponses, ProtocolLlama, ProtocolBasalt} {
		for _, mode := range []string{"execute", "plan"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				testSteerRequest(t, protocol, mode)
			})
		}
	}
}

func testSteerRequest(t *testing.T, protocol Protocol, mode string) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	attachment := ImagePart("image/png", picture.Bytes())
	calls := []ToolCall{
		{ID: "a", Type: "function", Function: FunctionCall{Name: "list_dir", Arguments: "{}"}},
		{ID: "b", Type: "function", Function: FunctionCall{Name: "list_dir", Arguments: "{}"}},
	}
	update := "Also check the image."
	if mode == "plan" {
		update = "Plan mode.\n\n" + update
	}
	messages := []Message{
		{Role: "system", Content: "Test session."},
		{Role: "user", Content: "List the files."},
		{Role: "assistant", ToolCalls: calls},
		{Role: "tool", Name: "list_dir", ToolCallID: "a", Content: "First"},
		{Role: "tool", Name: "list_dir", ToolCallID: "b", Content: "Second"},
		{Role: "user", Content: update, Update: mode == "execute"},
		{Role: "user", Parts: []Part{attachment}},
	}

	type item = map[string]any
	field, path := "messages", "/v1/chat/completions"
	frame := `{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`
	media := item{"type": "image_url", "image_url": item{"url": attachment.ImageURL.URL}}
	if protocol == ProtocolResponses {
		field, path = "input", "/v1/responses"
		frame = `{"type":"response.completed","response":{"status":"completed"}}`
		media = item{"type": "input_image", "image_url": attachment.ImageURL.URL}
	}
	result := func(id, text string) item {
		switch protocol {
		case ProtocolResponses:
			return item{"type": "function_call_output", "call_id": id, "output": text}
		default:
			return item{"role": "tool", "tool_call_id": id, "name": "list_dir", "content": text}
		}
	}
	want := []item{result("a", "First")}
	if mode == "plan" || protocol == ProtocolResponses {
		want = append(want, result("b", "Second"), item{"role": "user", "content": update})
	} else {
		want = append(want, result("b", "Second\n\n"+update))
	}
	want = append(want, item{"role": "user", "content": []any{media}})

	captures := make(chan []json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		var items []json.RawMessage
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if err := json.Unmarshal(body[field], &items); err != nil {
			t.Error(err)
		}
		captures <- items
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
	}))
	defer server.Close()
	req := ChatRequest{Model: "test", Messages: messages, Effort: EffortAuto,
		Tools: []Tool{{Type: "function", Function: FunctionSpec{Name: "list_dir", Parameters: json.RawMessage(`{"type":"object"}`)}}}}
	client := New(server.URL, "", protocol)
	send := func(req ChatRequest) []json.RawMessage {
		t.Helper()
		if _, err := client.Chat(t.Context(), req, nil); err != nil {
			t.Fatal(err)
		}
		return <-captures
	}
	first := send(req)
	if len(first) != 3+len(want) {
		t.Fatalf("got %d items, want %d", len(first), 3+len(want))
	}
	for i, expected := range want {
		var got item
		if err := json.Unmarshal(first[3+i], &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("item %d = %v, want %v", i, got, expected)
		}
	}
	req.Messages = append(messages, Message{Role: "assistant", Content: "Done."}, Message{Role: "user", Content: "Continue."})
	next := send(req)
	if len(next) != len(first)+2 || !reflect.DeepEqual(first, next[:len(first)]) {
		t.Fatal("appending another turn changed the earlier wire items")
	}
}

func TestChatUpdateFollowsTheLastToolPart(t *testing.T) {
	image := ImagePart("image/png", []byte("output"))
	parts := []Part{TextPart("result"), image, TextPart("spare")}
	messages := []Message{
		{Role: "tool", ToolCallID: "b", Parts: parts[:2]},
		{Role: "user", Content: "update", Update: true},
	}
	got := chatMessages(messages)
	want := []Part{parts[0], image, TextPart("\n\nupdate")}
	if len(got) != 1 || got[0].ToolCallID != "b" || !reflect.DeepEqual(got[0].Parts, want) {
		t.Fatalf("tool message = %v", got)
	}
	if parts[2].Text != "spare" {
		t.Fatal("the chat request changed the source parts")
	}
}
