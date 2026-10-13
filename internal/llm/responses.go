package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// responsesBackend speaks the OpenAI responses endpoint. It lists models the
// same way an OpenAI-compatible server does, and differs entirely in how a
// turn is asked for: the conversation travels as a flat list of input items
// rather than as messages, and the reply arrives as named events rather than
// as completion chunks.
//
// No probe resolves to this backend. An address serving it also serves the
// chat endpoint and the same model list, so which of the two the harness
// should use is a choice, not a discovery.
type responsesBackend struct{ c *Client }

// Detect reports the protocol it was told to speak. The model list is the
// only thing to ask for, and it says nothing about which endpoint generates.
func (b *responsesBackend) Detect(ctx context.Context) (Info, error) {
	models, err := b.Models(ctx)
	if err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	info := Info{Mode: ModeResponses}
	if len(models) == 1 {
		info.Model, info.NCtx = models[0].ID, models[0].CtxHint
	}
	return info, nil
}

func (b *responsesBackend) Models(ctx context.Context) ([]Model, error) {
	return listOpenAIModels(ctx, b.c)
}

// Prepare has nothing to start, so it reports what the model list says and
// returns.
func (b *responsesBackend) Prepare(ctx context.Context, model string) (Ready, error) {
	p, err := b.Props(ctx, model)
	if err != nil {
		return Ready{}, err
	}
	return ready(p, "models"), nil
}

func (b *responsesBackend) Props(ctx context.Context, model string) (Props, error) {
	return openAIProps(ctx, b.c, model)
}

// Chat streams one turn. The reply is decoded by responsesState, since the
// frames this endpoint sends are named events rather than completion chunks.
func (b *responsesBackend) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	body, err := buildResponsesBody(req)
	if err != nil {
		return Result{}, buildError(err)
	}
	res, err := b.c.stream(ctx, responsesPath, body, req.Model, newResponsesState(), on)
	return res, markOverflow(err, openAIOverflow)
}

// buildResponsesBody renders the wire request. Thinking travels as an effort
// on a reasoning object; the levels are the API's own apart from off, which
// is its "none". Auto sends no reasoning object, which for a model that
// reasons by default is not the same as off.
//
// A summary is asked for whenever thinking is. This endpoint never returns the
// thinking itself: the reasoning items it sends back are opaque, and a summary
// is the only readable form of it there is. Some accounts and models produce
// none even when asked, and the turn is no worse for it.
//
// The turn is never stored: store is false and every item is sent again each
// time, so the harness holds the conversation and the endpoint holds nothing.
func buildResponsesBody(req ChatRequest) ([]byte, error) {
	instructions, input := toResponsesInput(req.Messages)
	body := map[string]any{
		"model":  req.Model,
		"input":  input,
		"stream": true,
		"store":  false,
	}
	if instructions != "" {
		body["instructions"] = instructions
	}
	if len(req.Tools) > 0 {
		body["tools"] = toResponsesTools(req.Tools)
		body["tool_choice"] = "auto"
	}
	// The cap is named for the output rather than for the completion, and it
	// counts the thinking a reasoning model does before it answers.
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	switch req.Effort {
	case EffortAuto:
	case EffortOff:
		body["reasoning"] = map[string]any{"effort": "none"}
	default:
		body["reasoning"] = map[string]any{"effort": string(req.Effort), "summary": "auto"}
	}
	return json.Marshal(body)
}

// toResponsesTools flattens a tool. This endpoint puts the name and the
// schema on the tool itself rather than inside a function object.
func toResponsesTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t.Function.Name,
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
		})
	}
	return out
}

// toResponsesInput turns the conversation into the flat list of items this
// endpoint takes, and lifts the system message out of it: the endpoint has a
// field of its own for that and no system role in the list.
//
// A call and its result are items in their own right rather than parts of an
// assistant and a tool message, so an assistant turn that both spoke and
// called becomes two items.
func toResponsesInput(msgs []Message) (string, []map[string]any) {
	var instructions strings.Builder
	items := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system":
			if instructions.Len() > 0 {
				instructions.WriteString("\n\n")
			}
			instructions.WriteString(m.Content)
		case "tool":
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  userContent(m),
			})
		case "assistant":
			if m.Content != "" {
				items = append(items, map[string]any{
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": m.Content}},
				})
			}
			for _, tc := range m.ToolCalls {
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		default:
			items = append(items, map[string]any{"role": m.Role, "content": userContent(m)})
		}
	}
	return instructions.String(), items
}

// userContent is the text of a message, or the parts of one carrying an
// image. A call's output takes the same parts as a message does. The part types are this endpoint's own: what the chat endpoint calls
// text and image_url are input_text and input_image here.
func userContent(m Message) any {
	if len(m.Parts) == 0 {
		return m.Content
	}
	out := make([]map[string]any, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch {
		case p.Type == "text":
			out = append(out, map[string]any{"type": "input_text", "text": p.Text})
		case p.ImageURL != nil:
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
		}
	}
	return out
}

// responsesState folds a responses stream into a Result. The endpoint names
// every frame, so there is no accumulating of deltas by index: an item is
// announced, its fragments arrive, and it is closed.
//
// A call has two ids. The item id is what its argument fragments carry, the
// call id is what the result has to be sent back under, and the closing
// summary names only the second. Calls are gathered under the call id, with
// byItem carrying the first to the second, so a stream and its summary
// describing the same call produce one call rather than two.
type responsesState struct {
	res    Result
	acc    map[string]*partial
	byItem map[string]string
	order  []string
	fail   *Error
}

func newResponsesState() *responsesState {
	return &responsesState{acc: map[string]*partial{}, byItem: map[string]string{}}
}

func (s *responsesState) drop() { s.res.Dropped++ }

func (s *responsesState) failure() error {
	if s.fail == nil {
		return nil
	}
	return s.fail
}

// responseEvent is one named frame. Only the fields the harness reads are
// named; the endpoint sends a good deal more.
type responseEvent struct {
	Type     string          `json:"type"`
	Delta    *string         `json:"delta"`
	ItemID   *string         `json:"item_id"`
	Item     *responseItem   `json:"item"`
	Response *responseObject `json:"response"`
	// Timings is optional. A server that measures its own generation sends it
	// beside the response rather than inside it.
	Timings    *timings `json:"timings"`
	Message    *string  `json:"message"`
	Code       *string  `json:"code"`
	OutputText *string  `json:"text"`
}

type responseItem struct {
	ID        *string `json:"id"`
	Type      *string `json:"type"`
	CallID    *string `json:"call_id"`
	Name      *string `json:"name"`
	Arguments *string `json:"arguments"`
}

type responseObject struct {
	Status *string          `json:"status"`
	Output []responseOutput `json:"output"`
	Usage  *responsesUsage  `json:"usage"`
	// A server that ignored stream:true answers with one object, and the
	// timings it measured ride inside it rather than beside it.
	Timings           *timings `json:"timings"`
	IncompleteDetails *struct {
		Reason *string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message *string `json:"message"`
		Code    *string `json:"code"`
	} `json:"error"`
}

type responseOutput struct {
	Type      *string `json:"type"`
	CallID    *string `json:"call_id"`
	Name      *string `json:"name"`
	Arguments *string `json:"arguments"`
	Content   []struct {
		Type *string `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
}

type responsesUsage struct {
	InputTokens        *int `json:"input_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens *int `json:"output_tokens"`
	TotalTokens  *int `json:"total_tokens"`
}

func (u *responsesUsage) toUsage() *Usage {
	if u == nil {
		return nil
	}
	out := &Usage{
		PromptTokens:     deref(u.InputTokens),
		CompletionTokens: deref(u.OutputTokens),
		TotalTokens:      deref(u.TotalTokens),
	}
	if u.InputTokensDetails != nil {
		out.CachedTokens = u.InputTokensDetails.CachedTokens
	}
	return out
}

// dispatch decodes one named frame. The endpoint closes the stream with a
// completed, failed or incomplete response rather than with a sentinel, so
// there is no [DONE] to wait for.
func (s *responsesState) dispatch(payload []byte, on func(Event) error) (bool, error) {
	text := strings.TrimSpace(string(payload))
	if text == "[DONE]" {
		return true, nil
	}
	if !strings.HasPrefix(text, "{") {
		s.drop()
		return false, nil
	}
	var e responseEvent
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		s.drop()
		return false, nil
	}
	s.res.Chunks++
	return s.apply(&e, on)
}

func (s *responsesState) apply(e *responseEvent, on func(Event) error) (bool, error) {
	switch e.Type {
	case "response.output_text.delta":
		t := deref(e.Delta)
		if t == "" {
			return false, nil
		}
		s.res.Content += t
		return false, emit(on, Event{Kind: EventContent, Text: t})

	// Thinking arrives only as a summary, and only when the account may see
	// one. It is treated as the reasoning of the turn, which is what it is.
	case "response.reasoning_summary_text.delta":
		t := deref(e.Delta)
		if t == "" {
			return false, nil
		}
		s.res.Reasoning += t
		return false, emit(on, Event{Kind: EventReasoning, Text: t})

	case "response.output_item.added", "response.output_item.done":
		if e.Item == nil || deref(e.Item.Type) != "function_call" {
			return false, nil
		}
		// Both frames carry the whole item, so the closing one fills anything
		// the fragments never brought.
		key, p := s.open(deref(e.ItemID), deref(e.Item.ID), deref(e.Item.CallID))
		appendOnce(&p.id, fallback(deref(e.Item.CallID), key))
		appendOnce(&p.name, deref(e.Item.Name))
		if p.args.Len() == 0 {
			p.args.WriteString(deref(e.Item.Arguments))
		}
		return false, nil

	case "response.function_call_arguments.delta":
		key, p := s.open(deref(e.ItemID), "", "")
		args := deref(e.Delta)
		p.args.WriteString(args)
		return false, emit(on, Event{Kind: EventToolCall, Index: s.indexOf(key),
			ID: p.id.String(), Name: p.name.String(), Arguments: args})

	case "response.completed", "response.incomplete":
		return true, s.finish(e.Response, e.Timings, on)

	case "response.failed", "error":
		msg := deref(e.Message)
		if e.Response != nil && e.Response.Error != nil {
			msg = deref(e.Response.Error.Message)
		}
		s.fail = &Error{Kind: KindUpstream, Op: "chat",
			Message: "The model server failed: " + fallback(msg, "no reason given")}
		return true, nil
	}
	return false, nil
}

// finish reads the closing frame: the usage of the turn, why it stopped, and
// any item the deltas did not carry. tms is the timings the frame carries
// beside the response, and is nil on a server that measured nothing.
func (s *responsesState) finish(r *responseObject, tms *timings, on func(Event) error) error {
	if r == nil {
		return nil
	}
	for _, o := range r.Output {
		if deref(o.Type) != "function_call" {
			continue
		}
		key, p := s.open("", "", deref(o.CallID))
		appendOnce(&p.id, fallback(deref(o.CallID), key))
		appendOnce(&p.name, deref(o.Name))
		if p.args.Len() == 0 {
			p.args.WriteString(deref(o.Arguments))
		}
	}
	// A turn that stopped short says so on the response rather than on a
	// choice, and a reason of its own means the same as a chat finish reason.
	reason := "stop"
	if len(s.acc) > 0 {
		reason = "tool_calls"
	}
	if r.IncompleteDetails != nil && deref(r.IncompleteDetails.Reason) != "" {
		reason = deref(r.IncompleteDetails.Reason)
	}
	s.res.FinishReason = reason
	if err := emit(on, Event{Kind: EventFinish, FinishReason: reason}); err != nil {
		return err
	}
	if u := r.Usage.toUsage(); u != nil {
		s.res.Usage = u
		if err := emit(on, Event{Kind: EventUsage, Usage: u}); err != nil {
			return err
		}
	}
	// Timings arrive beside the response, or inside it from a server that
	// ignored stream:true.
	t := tms
	if t == nil {
		t = r.Timings
	}
	if t != nil {
		s.res.Timings = t.toTimings()
		return emit(on, Event{Kind: EventTimings, Timings: s.res.Timings})
	}
	return nil
}

// whole reads a reply from a server that ignored stream:true, which is one
// response object holding every item at once.
func (s *responsesState) whole(raw []byte, on func(Event) error) error {
	var r responseObject
	if err := json.Unmarshal(raw, &r); err != nil {
		s.drop()
		return nil
	}
	s.res.Chunks++
	for _, o := range r.Output {
		for _, c := range o.Content {
			if deref(c.Type) == "output_text" && deref(c.Text) != "" {
				s.res.Content += deref(c.Text)
				if err := emit(on, Event{Kind: EventContent, Text: deref(c.Text)}); err != nil {
					return err
				}
			}
		}
	}
	return s.finish(&r, nil, on)
}

// open is the accumulator for one call, under the call id when it is known
// and under the item id until it is. A frame naming both binds the two, so a
// later frame naming either finds what the other one started.
func (s *responsesState) open(itemID, altItemID, callID string) (string, *partial) {
	item := fallback(itemID, altItemID)
	key := callID
	if key == "" {
		key = s.byItem[item]
	}
	if key == "" {
		key = item
	}
	if item != "" && s.byItem[item] == "" {
		s.byItem[item] = key
		// The fragments arrived before the call id did, so what they were
		// gathered under becomes what the call is known by.
		if p := s.acc[item]; p != nil && item != key {
			s.rekey(item, key)
		}
	}
	if p := s.acc[key]; p != nil {
		return key, p
	}
	p := &partial{}
	s.acc[key] = p
	s.order = append(s.order, key)
	return key, p
}

// rekey moves an accumulator from the item id it was started under to the
// call id, keeping its place in the order.
func (s *responsesState) rekey(from, to string) {
	s.acc[to] = s.acc[from]
	delete(s.acc, from)
	for i, k := range s.order {
		if k == from {
			s.order[i] = to
		}
	}
}

func (s *responsesState) indexOf(itemID string) int {
	for i, k := range s.order {
		if k == itemID {
			return i
		}
	}
	return 0
}

func (s *responsesState) result() Result {
	res := s.res
	res.ToolCalls = nil
	for i, key := range s.order {
		p := s.acc[key]
		name := p.name.String()
		if name == "" {
			res.Dropped++
			continue
		}
		id := p.id.String()
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID: id, Type: "function",
			Function: FunctionCall{Name: name, Arguments: p.args.String()},
		})
	}
	return res
}
