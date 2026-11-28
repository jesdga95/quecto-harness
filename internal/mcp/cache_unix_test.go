//go:build darwin || linux

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSlowCacheReadsLeaveTheManagerAvailable(t *testing.T) {
	for _, op := range []string{"status", "offer"} {
		t.Run(op, func(t *testing.T) {
			s := fakeServer("remote")
			s.Command = "/no/such/server"
			m := newTestManager(t, s)
			if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := m.cachePath(s.Name)
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
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
			writer := fifoWriter(t, path)
			available := make(chan struct{})
			go func() {
				m.Server(s.Name)
				if op == "offer" {
					m.Configure(nil)
				}
				close(available)
			}()
			select {
			case <-available:
			case <-time.After(time.Second):
				t.Error("A cache read blocked the configuration lookup")
			}
			if _, err := fmt.Fprintf(writer, `{"launch":"%s","tools":[{"name":"old"}]}`, m.launchDigest(s, keyFor(s, ""))); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Cache reader did not finish")
			}
			if len(offered) > 0 {
				t.Errorf("Removed server offered %s", wires(offered))
			}
			select {
			case <-available:
			case <-time.After(time.Second):
				t.Fatal("Configuration lookup did not finish")
			}
		})
	}
}

func fifoWriter(t *testing.T, path string) *os.File {
	t.Helper()
	var writer *os.File
	waitFor(t, func() bool {
		var err error
		writer, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil && !errors.Is(err, syscall.ENXIO) {
			t.Fatal(err)
		}
		return err == nil
	})

	t.Cleanup(func() { writer.Close() })
	return writer
}

func TestSlowCatalogPreparationDoesNotDelayRemoval(t *testing.T) {
	srv := fakeServer("remote")
	m := newTestManager(t, srv)
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(m.cacheDir, ".catalog-key")
	if err := syscall.Mkfifo(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	in := m.instanceFor(srv, "")
	done := make(chan error, 1)
	go func() { done <- m.publishList(in, []Tool{{Name: "old"}}, 0) }()
	writer := fifoWriter(t, keyPath)
	removed := make(chan struct{})
	go func() { m.Configure(nil); close(removed) }()
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Error("Configuration waited for catalog preparation")
	}
	if _, err := writer.Write(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	select {
	case err := <-done:
		if !errors.Is(err, errConfigChanged) {
			t.Fatalf("Publish = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Catalog publication did not finish")
	}
	if _, err := os.Stat(m.cachePath(srv.Name)); !os.IsNotExist(err) {
		t.Fatalf("Removed catalog reappeared: %v", err)
	}
}
