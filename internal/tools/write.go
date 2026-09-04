package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"quecto-harness/internal/config"
)

// WriteFile replaces a file whole. The result given to the model is the
// summary line, not the content, so the session file does not carry it twice.
func (r *Runner) WriteFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args WriteFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("write_file: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("write_file needs a path")
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return Result{}, err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory", args.Path)
	}

	verb, perm := "wrote", os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		verb, perm = "replaced", info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, fmt.Errorf("create the folder for %s: check the permissions on the project", args.Path)
	}
	data := []byte(args.Content)
	if err := config.WriteAtomic(path, data, perm); err != nil {
		return Result{}, fmt.Errorf("write %s: %s", args.Path, err)
	}
	lines := strings.Count(args.Content, "\n")
	if len(args.Content) > 0 && !strings.HasSuffix(args.Content, "\n") {
		lines++
	}
	return Result{
		Output: fmt.Sprintf("%s %s (%d bytes, %d lines)", verb, r.rel(path), len(data), lines),
		Bytes:  len(data),
	}, nil
}
