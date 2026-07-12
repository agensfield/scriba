//go:build darwin || linux

package localapi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func socketPath(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "scriba-localapi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	d = filepath.Join(d, "private")
	return filepath.Join(d, "scriba.sock")
}

func TestListenModeAndRepeatedLifecycle(t *testing.T) {
	p := socketPath(t)
	for range 3 {
		l, err := Listen(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0600 {
			t.Fatalf("mode = %v", fi.Mode())
		}
		parent, _ := os.Stat(filepath.Dir(p))
		if parent.Mode().Perm() != 0700 {
			t.Fatalf("parent mode = %v", parent.Mode())
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("socket remains: %v", err)
		}
	}
}

func TestRefusesLiveSymlinkAndRegularFile(t *testing.T) {
	p := socketPath(t)
	l, err := Listen(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted live socket")
	}
	_ = l.Close()
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted regular file")
	}
	_ = os.Remove(p)
	if err := os.Symlink("elsewhere", p); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestRecoversStaleSocket(t *testing.T) {
	p := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := validSocket(fi)
	if !ok {
		t.Fatal("invalid test socket")
	}
	lock, _, err := acquire(p + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(lock, id); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	owned, err := Listen(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	_ = owned.Close()
}

func TestUnknownStaleSocketRefused(t *testing.T) {
	p := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted unknown socket")
	}
}

func TestWrongSocketModeAndLockSymlinkRefused(t *testing.T) {
	p := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = l.Close()
	if err := os.Chmod(p, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted wrong socket mode")
	}
	_ = os.Remove(p)
	_ = os.Remove(p + ".lock")
	if err := os.Symlink("target", p+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(context.Background(), p); err == nil {
		t.Fatal("accepted lock symlink")
	}
}

func TestConcurrentStartupHasOneOwner(t *testing.T) {
	p := socketPath(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan *Listener, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() { defer wg.Done(); <-start; l, err := Listen(context.Background(), p); results <- l; errs <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	owners := 0
	var owner *Listener
	for l := range results {
		if l != nil {
			owners++
			owner = l
		}
	}
	if owners != 1 {
		t.Fatalf("owners = %d", owners)
	}
	_ = owner.Close()
}

func TestContentionReturnsPromptly(t *testing.T) {
	p := socketPath(t)
	l, err := Listen(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	done := make(chan error, 1)
	go func() { _, err := Listen(context.Background(), p); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrActive) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("contention blocked")
	}
}

func TestConcurrentClose(t *testing.T) {
	p := socketPath(t)
	l, err := Listen(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Close() }()
	}
	wg.Wait()
}

func TestCloseDoesNotRemoveReplacement(t *testing.T) {
	p := socketPath(t)
	l, err := Listen(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("replacement removed: %v", err)
	}
}
