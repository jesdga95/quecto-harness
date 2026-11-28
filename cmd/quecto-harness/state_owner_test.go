package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeStateHelper(t *testing.T) {
	home := os.Getenv("QUECTO_TEST_STATE_DIR")
	if home == "" {
		t.Skip("Requires a temporary state directory")
	}
	if err := serve([]string{"--home", home, "--listen", "127.0.0.1:0", "--no-open"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// A successful start prints its URL. A refused start exits with a diagnostic.
func startStateOwner(t *testing.T, home string) (stop func(), refusal string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServeStateHelper$")
	cmd.Env = append(os.Environ(), "QUECTO_TEST_STATE_DIR="+home)
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "http://") {
		err = cmd.Wait()
		var refused *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &refused) || diagnostic.Len() == 0 {
			t.Fatalf("Unexpected startup result: %v (%s)", err, diagnostic.String())
		}
		return nil, diagnostic.String()
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		_ = cmd.Wait()
	}
	t.Cleanup(stop)
	return stop, ""
}

func TestStateAllowsOneServerAtATime(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	first, reason := startStateOwner(t, home)
	if first == nil {
		t.Fatalf("First server refused: %s", reason)
	}
	if second, _ := startStateOwner(t, home); second != nil {
		t.Error("Two servers accepted the same state directory")
		second()
	}
	first()
	if next, reason := startStateOwner(t, home); next == nil {
		t.Fatalf("State remained locked after its owner exited: %s", reason)
	}
}

func TestStateOwnersCannotShareTheSessionsDirectory(t *testing.T) {
	firstHome, secondHome := t.TempDir(), t.TempDir()
	owner, reason := startStateOwner(t, firstHome)
	if owner == nil {
		t.Fatal(reason)
	}
	if err := os.Symlink(filepath.Join(firstHome, "sessions"), filepath.Join(secondHome, "sessions")); err != nil {
		t.Fatal(err)
	}
	if second, reason := startStateOwner(t, secondHome); second != nil {
		second()
		t.Fatal("Shared sessions accepted a second writer")
	} else if !strings.Contains(reason, "sessions directory") || strings.Contains(reason, "--home") {
		t.Fatal(reason)
	}
	owner()
	if next, why := startStateOwner(t, secondHome); next == nil {
		t.Fatal(why)
	}
}

func TestSessionsMayResolveToTheStateHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Symlink(home, filepath.Join(home, "sessions")); err != nil {
		t.Fatal(err)
	}
	if owner, why := startStateOwner(t, home); owner == nil {
		t.Fatal(why)
	}
}
