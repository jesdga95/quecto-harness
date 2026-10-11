// Package tools executes read, read_image, grep, web_search, write,
// edit and bash. It knows nothing about who approved a call;
// the path guard is its only policy, and web_search has no path to guard,
// since it leaves the project rather than reading it.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/search"
)

// Runner executes the file and shell tools against one project root. All paths are
// resolved against Root and may not leave it.
type Runner struct {
	Root        string
	Shell       string
	Timeout     time.Duration
	MaxTimeout  time.Duration
	OutputLimit int
	ReadLimit   int
	ReadLines   int

	// Search is the provider web_search reaches, nil where none is configured.
	// SearchHits, SearchChars and SearchTimeout are its limits, read from the
	// same config the offer is computed from, so a tool that is sent and a
	// tool that is built cannot disagree about them.
	Search        search.Provider
	SearchHits    int
	SearchChars   int
	SearchTimeout time.Duration

	// Baseline receives each written file as it stood before the write, when
	// something is keeping them. Nil runs the tools with nothing recorded.
	Baseline func(Snapshot)

	// Seen is what the session has read, which write and edit check a file
	// against. Nil runs the tools with no such check.
	Seen *Seen

	// Hidden is the harness's own state directory, which no tool reaches even
	// when the project contains it: it holds the token and the allowlist.
	Hidden string

	// Spill is the folder where a command whose output was cut saves all of
	// it. read and grep reach it by absolute path; empty saves nothing.
	Spill string
}

// Hide sets the directory no tool may reach, resolved the way Root is.
func (r *Runner) Hide(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		r.Hidden = realPath(abs)
	}
}

// SpillTo sets the folder cut-off output is saved to. It may not exist yet;
// the first command that needs it creates it.
func (r *Runner) SpillTo(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		r.Spill = realPath(abs)
	}
}

// Result is what the model is told. Output is already truncated and, for
// bash, already carries the exit code line.
type Result struct {
	Output    string
	ExitCode  *int
	Truncated bool
	Bytes     int
	// Meta is what the transcript needs about the call that the sentence the
	// model reads does not carry.
	Meta map[string]any
	// Image is a picture sent to the model with Output, nil for text alone.
	Image *Image
}

// NewRunner resolves the root once, which is what makes the path guard work
// under a symlinked directory such as /tmp on macOS.
func NewRunner(root string, cfg config.Config) (*Runner, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open project %s: no such folder, or it cannot be read", root)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, fmt.Errorf("resolve project %s: %w", root, err)
	}
	timeout := cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	max := 4 * timeout
	if max < 600*time.Second {
		max = 600 * time.Second
	}
	limit := cfg.OutputLimitBytes
	if limit <= 0 {
		limit = 16384
	}
	shell := cfg.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	searchTimeout := cfg.Search.Timeout
	if searchTimeout <= 0 {
		searchTimeout = config.DefaultSearchTimeout
	}
	// The ask never exceeds what the output limit can carry: the endpoint
	// bills what it is asked for, and the result is cut to the limit after.
	return &Runner{
		Root:          real,
		Shell:         shell,
		Timeout:       timeout,
		MaxTimeout:    max,
		OutputLimit:   limit,
		ReadLimit:     64 << 10,
		ReadLines:     400,
		SearchHits:    cfg.Search.MaxHits,
		SearchChars:   min(cfg.Search.MaxChars, limit),
		SearchTimeout: searchTimeout,
	}, nil
}

type ReadFileArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type WriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ReplaceInFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type FindArgs struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Text string `json:"text"`
}

type RunShellArgs struct {
	Command  string `json:"command"`
	TimeoutS *int   `json:"timeout_s"`
	// Background starts the command and returns at once. It then has no
	// deadline, and its output is read as it arrives.
	Background bool `json:"background"`
}

// DecodeArgs reads a call's arguments. Unknown fields are ignored, since a small
// model routinely invents one and the call should still run. A field that
// arrived as the wrong kind of JSON value is reported by name, with the text the
// call put there and the kind the field takes. What is close to a boolean, such
// as "True" or 1, is still a wrong value, and the call is not run.
func DecodeArgs(tool string, raw json.RawMessage, v any) error {
	if strings.TrimSpace(string(raw)) == "" {
		return fmt.Errorf("%s: the arguments were empty", tool)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		if msg, ok := FieldError(raw, err); ok {
			return fmt.Errorf("%s: %s", tool, msg)
		}
		return fmt.Errorf("%s: the arguments are not valid JSON: %s", tool, err)
	}
	return nil
}

// FieldError renders a decode failure where one field arrived as the wrong kind
// of JSON value: the field, what was found at it and what it takes. It reports
// false for every other failure, which each caller answers in its own words.
func FieldError(raw json.RawMessage, err error) (string, bool) {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return "", false
	}
	got := jsonValue(typeErr.Value)
	text := rawAt(raw, strings.Split(typeErr.Field, "."), got)
	if text == "" {
		got = withArticle(got)
	} else {
		got = got + " " + text
	}
	return fmt.Sprintf("%s got %s, expected JSON %s", typeErr.Field, got, jsonKind(typeErr.Type.Kind())), true
}

// rawAt reads the text a call put at a dotted field path. Some Go releases name
// the array element in that path and some do not, so where the path crosses a
// list without an index the element holding a value of the failed kind is shown.
func rawAt(raw json.RawMessage, path []string, kind string) string {
	if len(path) == 0 {
		return clipText(raw)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		if next, ok := fields[path[0]]; ok {
			return rawAt(next, path[1:], kind)
		}
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		if i, err := strconv.Atoi(path[0]); err == nil {
			if i < 0 || i >= len(items) {
				return ""
			}
			return rawAt(items[i], path[1:], kind)
		}
		first := ""
		for _, item := range items {
			got := rawAt(item, path, kind)
			if got == "" {
				continue
			}
			if first == "" {
				first = got
			}
			if looksLike(got, kind) {
				return got
			}
		}
		return first
	}
	return ""
}

// looksLike reports whether raw text is of the kind the decoder named, which is
// how a field repeated in a list is matched to the element that failed.
func looksLike(text, kind string) bool {
	switch kind {
	case "string":
		return strings.HasPrefix(text, `"`)
	case "number":
		return text != "" && strings.ContainsAny(text[:1], "-0123456789")
	case "boolean":
		return text == "true" || text == "false"
	case "object":
		return strings.HasPrefix(text, "{")
	case "list":
		return strings.HasPrefix(text, "[")
	}
	return false
}

// clipText keeps the text shown back to a model short.
func clipText(raw json.RawMessage) string {
	r := []rune(strings.TrimSpace(string(raw)))
	if len(r) <= 80 {
		return string(r)
	}
	return string(r[:80]) + "..."
}

// withArticle reads a bare kind name as a phrase.
func withArticle(kind string) string {
	if kind != "" && strings.Contains("aeiou", kind[:1]) {
		return "an " + kind
	}
	return "a " + kind
}

// jsonKind names the JSON value a Go field of this kind decodes from.
func jsonKind(k reflect.Kind) string {
	switch k {
	case reflect.Bool:
		return "boolean true or false"
	case reflect.String:
		return "string"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	}
	return "another kind of value"
}

// jsonValue names the kind of JSON value the decoder found, as
// UnmarshalTypeError reports it.
func jsonValue(v string) string {
	switch v {
	case "bool":
		return "boolean"
	case "array":
		return "list"
	case "":
		return "another kind of value"
	}
	return v
}
