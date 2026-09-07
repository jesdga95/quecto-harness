package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type ErrKind string

const (
	KindUnreachable  ErrKind = "unreachable"
	KindAuth         ErrKind = "auth"
	KindNotFound     ErrKind = "not_found"
	KindBadRequest   ErrKind = "bad_request"
	KindModelLoading ErrKind = "model_loading"
	KindUpstream     ErrKind = "upstream"
	KindMalformed    ErrKind = "malformed"
	KindCanceled     ErrKind = "canceled"
)

// Error is every failure this package returns. Status is 0 when no response
// was received, and Body holds the first 8 KiB of a failing response.
type Error struct {
	Kind    ErrKind
	Status  int
	Message string
	Body    string
	Op      string
	err     error
}

// Error is the message as a person reads it. Op names the request that failed
// and stays out of it, since the string is shown in the transcript.
func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.err }

// Kind reports the kind of a model server error, or the empty string for any other
// error.
func Kind(err error) ErrKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// serverMessage digs the human-readable text out of a llama.cpp error body,
// falling back to the raw body when it is not the expected shape.
func serverMessage(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	var wrapper struct {
		Error *struct {
			Message *string `json:"message"`
		} `json:"error"`
		Message *string `json:"message"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapper); err == nil {
		if wrapper.Error != nil && wrapper.Error.Message != nil {
			return *wrapper.Error.Message
		}
		if wrapper.Message != nil {
			return *wrapper.Message
		}
	}
	if len(trimmed) > 300 {
		return trimmed[:300]
	}
	return trimmed
}

// classify turns a transport error or a non-2xx response into an *Error with
// a message that says what failed and what the reader can do about it.
func classify(op, baseURL, model string, status int, body string, err error) *Error {
	e := &Error{Op: op, Status: status, Body: body, err: err}
	msg := serverMessage(body)
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		e.Kind = KindCanceled
		e.Message = "The request was cancelled"
	case err != nil && isUnreachable(err):
		e.Kind = KindUnreachable
		e.Message = "Reach " + baseURL + ": the model server is not answering, check it is running and that server.url is right"
	case status == 401 || status == 403:
		e.Kind = KindAuth
		e.Message = "Authenticate with " + baseURL + ": the server rejected the token, set server.token in settings"
	case status == 404 || (status == 400 && strings.Contains(strings.ToLower(msg), "model") && strings.Contains(strings.ToLower(msg), "not found")):
		e.Kind = KindNotFound
		e.Message = "Model " + strconv.Quote(model) + " is not on " + baseURL + ": pick another from the model list"
	case status == 503 && strings.Contains(strings.ToLower(msg), "loading"):
		e.Kind = KindModelLoading
		e.Message = "Model " + strconv.Quote(model) + " is loading: send the message again in a moment"
	case status >= 400 && status < 500:
		e.Kind = KindBadRequest
		e.Message = "The model server refused the request: " + fallback(msg, "no reason given")
	case status >= 500:
		e.Kind = KindUpstream
		e.Message = "The model server failed: " + fallback(msg, "status "+strconv.Itoa(status))
	case err != nil:
		e.Kind = KindUnreachable
		e.Message = "Reach " + baseURL + ": " + err.Error()
	default:
		e.Kind = KindUpstream
		e.Message = "The model server failed: " + fallback(msg, "status "+strconv.Itoa(status))
	}
	return e
}

func fallback(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func isUnreachable(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func earlyCloseError(op string) *Error {
	return &Error{Kind: KindUpstream, Op: op,
		Message: "The model server closed the stream early: send the message again"}
}

func malformedError(op string, dropped int) *Error {
	return &Error{Kind: KindMalformed, Op: op,
		Message: fmt.Sprintf("The model server sent nothing usable: %d frames could not be read", dropped)}
}
