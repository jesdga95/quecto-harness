//go:build darwin || linux

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"testing"
	"time"
)

// pauseRead holds one disk read until the test releases it.
func pauseRead(m *Manager, path string) (<-chan struct{}, func()) {
	started, resume := make(chan struct{}), make(chan struct{})
	read := m.readFile
	m.readFile = func(name string) ([]byte, error) {
		data, err := read(name)
		if name == path {
			close(started)
			<-resume
		}
		return data, err
	}
	return started, sync.OnceFunc(func() { close(resume) })
}

func awaitSignal(t *testing.T, ch <-chan struct{}, action string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for %s", action)
	}
}

func TestSlowCacheReadsLeaveTheManagerAvailable(t *testing.T) {
	for _, op := range []string{"status", "offer"} {
		t.Run(op, func(t *testing.T) {
			s := fakeServer("remote")
			s.Command = "/no/such/server"
			m := newTestManager(t, s)
			seedCache(t, m, s, "old")
			started, release := pauseRead(m, m.cachePath(s.Name))
			defer release()
			var offered []Offered
			done := make(chan struct{})
			go func() {
				defer close(done)
				if op == "status" {
					m.Status()
				} else {
					offered, _ = m.Tools(context.Background(), "")
				}
			}()
			awaitSignal(t, started, "the cache read")
			available := make(chan struct{})
			go func() {
				m.Server(s.Name)
				if op == "offer" {
					m.Configure(nil)
				}
				close(available)
			}()
			awaitSignal(t, available, "the configuration lookup")
			release()
			awaitSignal(t, done, "the cache reader")
			if len(offered) != 0 {
				t.Fatalf("Removed server offered %s", wires(offered))
			}
		})
	}
}

func TestSlowCatalogPreparationDoesNotDelayRemoval(t *testing.T) {
	s := fakeServer("remote")
	m := newTestManager(t, s)
	path := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	started, release := pauseRead(m, path)
	defer release()
	in := m.instanceFor(s, "")
	done := make(chan error, 1)
	go func() { done <- m.publishList(in, []Tool{{Name: "old"}}, 0) }()
	awaitSignal(t, started, "catalog preparation")
	removed := make(chan struct{})
	go func() { m.Configure(nil); close(removed) }()
	awaitSignal(t, removed, "server removal")
	release()
	select {
	case err := <-done:
		if !errors.Is(err, errConfigChanged) {
			t.Fatalf("Publish = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Catalog publication did not finish")
	}
	if _, err := os.Stat(m.cachePath(s.Name)); !os.IsNotExist(err) {
		t.Fatalf("Removed catalog reappeared: %v", err)
	}
}
