//go:build darwin || linux

package mcp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

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
	started, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	hold := sync.OnceFunc(func() { close(started); <-resume })
	read := m.readKey
	m.readKey = func(f *os.File) ([]byte, error) { hold(); return read(f) }
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

func TestKeyPublicationWaitsForTheVolumeLock(t *testing.T) {
	m := newTestManager(t)
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.cacheDir, ".catalog-key")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockCatalogKey(lock); err != nil {
		t.Fatal(err)
	}
	atLink := make(chan struct{})
	m.linkKey = func(string, string) error { close(atLink); return syscall.ENOTSUP }
	result := make(chan []byte, 1)
	go func() { result <- m.cacheKey() }()
	awaitSignal(t, atLink, "the hard link attempt")
	select {
	case <-result:
		t.Fatal("Published a key while another creator held the lock")
	case <-time.After(150 * time.Millisecond):
	}
	want := bytes.Repeat([]byte{42}, 32)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if !bytes.Equal(got, want) {
			t.Fatal("Replaced the first creator's key")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Creator did not finish")
	}
}
