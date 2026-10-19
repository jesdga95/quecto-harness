package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxFrameBytes caps one SSE event payload. A larger frame is dropped and the
// parser resynchronises at the next blank line.
const maxFrameBytes = 1 << 20

// How long a reply may go silent before it is abandoned. The first limit holds
// until the reply carries output, which covers prompt processing and thinking
// that is not streamed; after that, silence is a dead connection.
var (
	streamFirstEvent = 5 * time.Minute
	streamIdle       = 90 * time.Second
)

type EventKind int

const (
	EventContent EventKind = iota
	EventReasoning
	EventToolCall
	EventFinish
	EventUsage
	EventTimings
)

// Event is one decoded piece of a stream. Only the fields relevant to Kind
// are set. Arguments carries the fragment from this chunk alone.
type Event struct {
	Kind EventKind

	Text string

	Index     int
	ID        string
	Name      string
	Arguments string

	FinishReason string
	Usage        *Usage
	Timings      *Timings
}

// Result is the assembled turn. Chunks and Dropped count frames that decoded
// and frames that did not.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
	Timings      *Timings
	Chunks       int
	Dropped      int
}

// toolCallMarkers open the tool call syntaxes a model writes when the server
// parsed none out of the text. The list is fixed, so a syntax not named here
// reads as ordinary content rather than being guessed at.
var toolCallMarkers = []string{"<tool_call>", "<function=", "[TOOL_CALLS]", "<|python_tag|>"}

// CutOff reports whether a finish reason says the reply hit its token limit.
// Chat completions says length and the Responses API names the limit.
func CutOff(reason string) bool {
	return reason == "length" || reason == "max_output_tokens"
}

// UnparsedToolCall reports whether text carries a tool call the server left in
// the content. A turn that ends on one ended on a failure, not on an answer.
func UnparsedToolCall(text string) bool {
	for _, m := range toolCallMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// partial accumulates one tool call across fragments. Builds resend the id
// and the name inconsistently, so both are accumulated defensively.
type partial struct {
	id   strings.Builder
	name strings.Builder
	args strings.Builder
}

// sink folds the frames of one stream into a Result. Each protocol brings its
// own, because a completion chunk and a named response event are read
// differently even though they arrive down the same pipe.
type sink interface {
	// dispatch decodes one payload and reports whether the stream is over.
	dispatch(payload []byte, on func(Event) error) (bool, error)
	// whole reads a reply from a server that ignored stream:true.
	whole(raw []byte, on func(Event) error) error
	// drop counts a frame that could not be read.
	drop()
	// failure is the error the server sent mid-stream, or nil.
	failure() error
	result() Result
}

// stream sends one rendered request to path and decodes the reply with st. on
// is called synchronously from the read loop, in stream order; returning an
// error from it aborts the request.
func (c *Client) stream(ctx context.Context, path string, body []byte, model string, st sink, on func(Event) error) (Result, error) {
	// The request takes a context of its own so the idle deadline can end it
	// without the caller's context having been cancelled.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, classify("chat", c.BaseURL, model, 0, "", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-store")
	c.authorize(httpReq)

	resp, err := c.send(httpReq)
	if err != nil {
		return Result{}, classify("chat", c.BaseURL, model, 0, "", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return Result{}, classify("chat", c.BaseURL, model, resp.StatusCode, string(raw), nil)
	}

	idle := newIdleReader(resp.Body, streamFirstEvent, streamIdle, cancel)
	defer idle.stop()
	// Frames that open a reply, such as response.created, are not output, so
	// only text, thinking or a tool call moves the reply onto the shorter limit.
	watched := func(ev Event) error {
		if carriesOutput(ev.Kind) {
			idle.output()
		}
		return emit(on, ev)
	}

	var res Result
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		res, err = c.readStream(ctx, idle, st, watched)
	} else {
		res, err = c.readWhole(idle, st, watched)
	}
	// A read that ended while the caller is still waiting ended on the idle
	// deadline, which reads as a cancellation everywhere below this point.
	if err != nil && ctx.Err() == nil {
		if waited, quiet := idle.stalled(); quiet {
			return res, stalledError("chat", waited)
		}
	}
	return res, err
}

// idleReader ends a reply that stops arriving. The deadline is reset by every
// read that returns bytes, and drops to rest once the reply carries output, so
// a long wait before the output starts does not license one in the middle of it.
type idleReader struct {
	r    io.Reader
	rest time.Duration

	mu    sync.Mutex
	limit time.Duration
	quiet bool
	timer *time.Timer
}

func newIdleReader(r io.Reader, first, rest time.Duration, onIdle func()) *idleReader {
	ir := &idleReader{r: r, rest: rest, limit: first}
	ir.timer = time.AfterFunc(first, func() {
		ir.mu.Lock()
		ir.quiet = true
		ir.mu.Unlock()
		onIdle()
	})
	return ir
}

func (ir *idleReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.mu.Lock()
		if !ir.quiet {
			ir.timer.Reset(ir.limit)
		}
		ir.mu.Unlock()
	}
	return n, err
}

// output moves the deadline to rest, since the reply has started to carry
// output and a gap from here on falls between tokens.
func (ir *idleReader) output() {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	if !ir.quiet && ir.limit != ir.rest {
		ir.limit = ir.rest
		ir.timer.Reset(ir.rest)
	}
}

// carriesOutput reports whether an event is part of the generation, as opposed
// to a frame that opens or closes the reply.
func carriesOutput(kind EventKind) bool {
	switch kind {
	case EventContent, EventReasoning, EventToolCall:
		return true
	}
	return false
}

// stalled reports that the deadline passed with nothing arriving, and how
// long that deadline was.
func (ir *idleReader) stalled() (time.Duration, bool) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	return ir.limit, ir.quiet
}

func (ir *idleReader) stop() { ir.timer.Stop() }

// readWhole handles a server that ignored stream:true and answered with one
// object, converting it into the same event sequence.
func (c *Client) readWhole(body io.Reader, st sink, on func(Event) error) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 32<<20))
	if err != nil {
		return Result{}, &Error{Kind: KindMalformed, Op: "chat", err: err,
			Message: "Read the reply: " + err.Error()}
	}
	if err := st.whole(raw, on); err != nil {
		return st.result(), err
	}
	res := st.result()
	if res.Chunks == 0 {
		return res, malformedError("chat", res.Dropped)
	}
	return res, nil
}

type streamState struct {
	res     Result
	acc     map[int]*partial
	order   []int
	lastIdx int
	fail    *Error
	done    bool
}

func newStreamState() *streamState {
	return &streamState{acc: map[int]*partial{}, lastIdx: -1}
}

// readStream drives the frame state machine over the response body. A
// malformed frame is counted and skipped; it never ends the stream.
func (c *Client) readStream(ctx context.Context, body io.Reader, st sink, on func(Event) error) (Result, error) {
	r := bufio.NewReaderSize(body, 64<<10)

	const (
		sIdle = iota
		sEvent
		sSkip
		sDone
	)
	state := sIdle
	var buf []byte

	for state != sDone {
		if err := ctx.Err(); err != nil {
			return st.result(), classify("chat", c.BaseURL, "", 0, "", err)
		}
		line, tooLong, err := readLine(r, maxFrameBytes)
		if err != nil {
			if err != io.EOF {
				if ctx.Err() != nil {
					return st.result(), classify("chat", c.BaseURL, "", 0, "", ctx.Err())
				}
				res := st.result()
				if res.Chunks == 0 {
					return res, malformedError("chat", res.Dropped)
				}
				return res, earlyCloseError("chat")
			}
			break
		}
		if tooLong {
			st.drop()
			buf = nil
			state = sSkip
			continue
		}
		switch state {
		case sIdle:
			switch {
			case len(line) == 0 || line[0] == ':':
			case hasField(line, "data"):
				buf = append(buf[:0], fieldValue(line, "data")...)
				state = sEvent
			case hasField(line, "event"), hasField(line, "id"), hasField(line, "retry"):
			default:
				st.drop()
			}
		case sEvent:
			switch {
			case len(line) == 0:
				stop, err := st.dispatch(buf, on)
				buf = buf[:0]
				if err != nil {
					return st.result(), err
				}
				if stop {
					state = sDone
				} else {
					state = sIdle
				}
			case hasField(line, "data"):
				buf = append(buf, '\n')
				buf = append(buf, fieldValue(line, "data")...)
				if len(buf) > maxFrameBytes {
					st.drop()
					buf = buf[:0]
					state = sSkip
				}
			}
		case sSkip:
			if len(line) == 0 {
				state = sIdle
			}
		}
	}

	if state == sEvent && len(buf) > 0 {
		if _, err := st.dispatch(buf, on); err != nil {
			return st.result(), err
		}
	}

	res := st.result()
	if err := st.failure(); err != nil {
		return res, err
	}
	if state == sDone {
		return res, nil
	}
	switch {
	case res.Chunks > 0 && res.FinishReason != "":
		return res, nil
	case res.Chunks > 0:
		return res, earlyCloseError("chat")
	default:
		return res, malformedError("chat", res.Dropped)
	}
}

func (s *streamState) drop() { s.res.Dropped++ }

func (s *streamState) failure() error {
	if s.fail == nil {
		return nil
	}
	return s.fail
}

// whole reads a single completion object, which is what a server that ignored
// stream:true sends.
func (s *streamState) whole(raw []byte, on func(Event) error) error {
	var ch chunk
	if err := json.Unmarshal(raw, &ch); err != nil {
		s.drop()
		return nil
	}
	return s.apply(&ch, on)
}

// dispatch decodes one buffered payload. It reports whether the stream is
// finished, which is either [DONE] or a mid-stream error object.
func (s *streamState) dispatch(payload []byte, on func(Event) error) (bool, error) {
	text := strings.TrimSpace(string(payload))
	if text == "[DONE]" {
		return true, nil
	}
	if !strings.HasPrefix(text, "{") {
		s.res.Dropped++
		return false, nil
	}
	var ch chunk
	if err := json.Unmarshal([]byte(text), &ch); err != nil {
		s.res.Dropped++
		return false, nil
	}
	if err := s.apply(&ch, on); err != nil {
		return true, err
	}
	if s.fail != nil {
		return true, nil
	}
	return false, nil
}

// apply emits the events of one decoded chunk in stream order: reasoning,
// content, tool-call fragments, finish reason, usage, timings.
func (s *streamState) apply(ch *chunk, on func(Event) error) error {
	if ch.Error != nil {
		s.fail = &Error{Kind: KindUpstream, Op: "chat", Status: deref(ch.Error.Code),
			Message: "The model server failed: " + fallback(deref(ch.Error.Message), "no reason given")}
		return nil
	}
	s.res.Chunks++

	if len(ch.Choices) > 0 {
		c := ch.Choices[0]
		d := c.Delta
		if d == nil {
			d = c.Message
		}
		if d != nil {
			reasoning := deref(d.ReasoningContent)
			if reasoning == "" {
				reasoning = deref(d.Reasoning)
			}
			if reasoning != "" {
				s.res.Reasoning += reasoning
				if err := emit(on, Event{Kind: EventReasoning, Text: reasoning}); err != nil {
					return err
				}
			}
			if content := deref(d.Content); content != "" {
				s.res.Content += content
				if err := emit(on, Event{Kind: EventContent, Text: content}); err != nil {
					return err
				}
			}
			for _, tc := range d.ToolCalls {
				if err := s.applyToolCall(tc, on); err != nil {
					return err
				}
			}
		}
		if fr := deref(c.FinishReason); fr != "" {
			s.res.FinishReason = fr
			if err := emit(on, Event{Kind: EventFinish, FinishReason: fr}); err != nil {
				return err
			}
		}
	}

	if u := ch.Usage.toUsage(); u != nil {
		s.res.Usage = u
		if err := emit(on, Event{Kind: EventUsage, Usage: u}); err != nil {
			return err
		}
	}
	if t := ch.Timings.toTimings(); t != nil {
		s.res.Timings = t
		if err := emit(on, Event{Kind: EventTimings, Timings: t}); err != nil {
			return err
		}
	}
	return nil
}

// applyToolCall folds one fragment into the accumulator. An absent index
// means the server streams a single call without them, so the last index
// seen is reused.
func (s *streamState) applyToolCall(tc deltaToolCall, on func(Event) error) error {
	idx := s.lastIdx
	if tc.Index != nil {
		idx = *tc.Index
	} else if idx < 0 {
		idx = 0
	}
	s.lastIdx = idx

	p := s.acc[idx]
	if p == nil {
		p = &partial{}
		s.acc[idx] = p
		s.order = append(s.order, idx)
	}
	appendOnce(&p.id, deref(tc.ID))
	var args string
	if tc.Function != nil {
		appendOnce(&p.name, deref(tc.Function.Name))
		args = deref(tc.Function.Arguments)
		p.args.WriteString(args)
	}
	return emit(on, Event{Kind: EventToolCall, Index: idx,
		ID: p.id.String(), Name: p.name.String(), Arguments: args})
}

// appendOnce skips a fragment that repeats the whole of what is there, which
// is how a build that resends the id or the name on every fragment is handled.
// A piece of a name often occurs in the part already assembled, so anything
// short of the whole is appended.
func appendOnce(b *strings.Builder, s string) {
	if s == "" || b.String() == s {
		return
	}
	b.WriteString(s)
}

func (s *streamState) result() Result {
	res := s.res
	res.ToolCalls = nil
	for _, idx := range s.order {
		p := s.acc[idx]
		name := p.name.String()
		if name == "" {
			res.Dropped++
			continue
		}
		id := p.id.String()
		if id == "" {
			id = fmt.Sprintf("call_%d", idx)
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID:       id,
			Type:     "function",
			Function: FunctionCall{Name: name, Arguments: p.args.String()},
		})
	}
	return res
}

func emit(on func(Event) error, ev Event) error {
	if on == nil {
		return nil
	}
	return on(ev)
}

// hasField reports whether line is the named SSE field, with or without the
// optional space after the colon.
func hasField(line []byte, name string) bool {
	if !bytes.HasPrefix(line, []byte(name)) {
		return false
	}
	rest := line[len(name):]
	return len(rest) == 0 || rest[0] == ':'
}

func fieldValue(line []byte, name string) []byte {
	rest := line[len(name):]
	if len(rest) > 0 && rest[0] == ':' {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return rest
}

// readLine returns one line without its terminator. tooLong is true when the
// line exceeded limit, in which case the rest of it has been discarded.
func readLine(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var acc []byte
	for {
		slice, e := r.ReadSlice('\n')
		if e == bufio.ErrBufferFull {
			if len(acc)+len(slice) > limit {
				if err := discardLine(r); err != nil {
					return nil, true, err
				}
				return nil, true, nil
			}
			acc = append(acc, slice...)
			continue
		}
		if e != nil {
			if e == io.EOF && (len(acc) > 0 || len(slice) > 0) {
				acc = append(acc, slice...)
				return trimEOL(acc), false, nil
			}
			return nil, false, e
		}
		acc = append(acc, slice...)
		if len(acc) > limit {
			return nil, true, nil
		}
		return trimEOL(acc), false, nil
	}
}

func discardLine(r *bufio.Reader) error {
	for {
		_, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			continue
		}
		return err
	}
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}
