//go:build darwin || linux

// Package localapi owns Scriba's local Unix socket. Security assumes a
// cooperative same-user namespace protected by a trusted 0700 parent; Unix
// path operations cannot prevent a hostile same-UID process from racing unlink.
package localapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var ErrActive = errors.New("localapi: listener active")

type ActiveError struct{ Path string }

func (e *ActiveError) Error() string { return fmt.Sprintf("localapi: listener active at %q", e.Path) }
func (e *ActiveError) Unwrap() error { return ErrActive }

type identity struct{ dev, ino uint64 }

type Listener struct {
	*net.UnixListener
	path     string
	id       identity
	lock     *os.File
	once     sync.Once
	closeErr error
}

func Listen(ctx context.Context, path string) (*Listener, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if path == "" || filepath.Base(path) == "." {
		return nil, fmt.Errorf("localapi: invalid socket path")
	}
	parent := filepath.Dir(path)
	parentID, err := ensureParent(parent)
	if err != nil {
		return nil, err
	}
	lock, recorded, err := acquire(path + ".lock")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Listener, error) { return nil, errors.Join(e, lock.Close()) }

	if fi, e := os.Lstat(path); e == nil {
		id, valid := validSocket(fi)
		if !valid {
			return fail(fmt.Errorf("localapi: existing path is not an owned 0600 socket"))
		}
		if recorded == nil || *recorded != id {
			return fail(fmt.Errorf("localapi: existing socket identity is not proven stale"))
		}
		if err := revalidateParent(parent, parentID); err != nil {
			return fail(err)
		}
		fi2, e := os.Lstat(path)
		id2, valid2 := validSocket(fi2)
		if e != nil || !valid2 || id2 != id {
			return fail(fmt.Errorf("localapi: socket changed before stale removal"))
		}
		if e := os.Remove(path); e != nil {
			return fail(fmt.Errorf("localapi: remove stale socket: %w", e))
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return fail(e)
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fail(fmt.Errorf("localapi: listen %q: %w", path, err))
	}
	ln.SetUnlinkOnClose(false)
	cleanup := func(primary error, id *identity) (*Listener, error) {
		var unlink error
		if id != nil {
			unlink = removeIfIdentity(path, *id)
		}
		return nil, errors.Join(primary, ln.Close(), unlink, lock.Close())
	}
	createdFI, err := os.Lstat(path)
	if err != nil {
		return cleanup(err, nil)
	}
	createdID, err := fileIdentity(createdFI)
	if err != nil || createdFI.Mode()&os.ModeSocket == 0 {
		return cleanup(fmt.Errorf("localapi: bound path identity unavailable"), nil)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return cleanup(err, &createdID)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return cleanup(err, &createdID)
	}
	id, valid := validSocket(fi)
	if !valid || id != createdID {
		return cleanup(fmt.Errorf("localapi: bound path failed verification"), &createdID)
	}
	if err := revalidateParent(parent, parentID); err != nil {
		return cleanup(err, &id)
	}
	if err := writeIdentity(lock, id); err != nil {
		return cleanup(err, &id)
	}
	return &Listener{UnixListener: ln, path: path, id: id, lock: lock}, nil
}

func ensureParent(path string) (identity, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return identity{}, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return identity{}, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return identity{}, fmt.Errorf("localapi: parent is not a real directory")
	}
	if err := verifyOwned(fi, 0700); err != nil {
		return identity{}, err
	}
	return fileIdentity(fi)
}
func revalidateParent(path string, want identity) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	id, e := fileIdentity(fi)
	if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || id != want {
		return fmt.Errorf("localapi: parent directory changed")
	}
	return verifyOwned(fi, 0700)
}
func verifyOwned(fi os.FileInfo, mode os.FileMode) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("localapi: unavailable identity")
	}
	if int64(st.Uid) != int64(os.Getuid()) {
		return fmt.Errorf("localapi: foreign owner")
	}
	if fi.Mode().Perm() != mode {
		return fmt.Errorf("localapi: unsafe mode %04o", fi.Mode().Perm())
	}
	return nil
}
func fileIdentity(fi os.FileInfo) (identity, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return identity{}, fmt.Errorf("localapi: unavailable identity")
	}
	dev, devErr := strconv.ParseUint(fmt.Sprint(st.Dev), 10, 64)
	ino, inoErr := strconv.ParseUint(fmt.Sprint(st.Ino), 10, 64)
	if devErr != nil || inoErr != nil {
		return identity{}, fmt.Errorf("localapi: invalid identity")
	}
	return identity{dev, ino}, nil
}
func validSocket(fi os.FileInfo) (identity, bool) {
	if fi == nil || fi.Mode()&os.ModeSocket == 0 || verifyOwned(fi, 0600) != nil {
		return identity{}, false
	}
	id, e := fileIdentity(fi)
	return id, e == nil
}

func acquire(path string) (*os.File, *identity, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("localapi: open lease: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	bad := func(e error) (*os.File, *identity, error) { return nil, nil, errors.Join(e, f.Close()) }
	fi, err := f.Stat()
	if err != nil {
		return bad(err)
	}
	if !fi.Mode().IsRegular() || verifyOwned(fi, 0600) != nil {
		return bad(fmt.Errorf("localapi: invalid lease file"))
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil, errors.Join(&ActiveError{Path: strings.TrimSuffix(path, ".lock")}, f.Close())
		}
		return bad(err)
	}
	pathFI, err := os.Lstat(path)
	if err != nil {
		return bad(err)
	}
	a, _ := fileIdentity(fi)
	b, _ := fileIdentity(pathFI)
	if a != b || pathFI.Mode()&os.ModeSymlink != 0 {
		return bad(fmt.Errorf("localapi: lease path changed"))
	}
	recorded, err := readIdentity(f)
	if err != nil {
		return bad(err)
	}
	return f, recorded, nil
}
func readIdentity(f *os.File) (*identity, error) {
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return nil, e
	}
	b, e := io.ReadAll(io.LimitReader(f, 128))
	if e != nil {
		return nil, e
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil, nil
	}
	p := strings.Fields(s)
	if len(p) != 2 {
		return nil, fmt.Errorf("localapi: invalid lease metadata")
	}
	d, e1 := strconv.ParseUint(p[0], 10, 64)
	i, e2 := strconv.ParseUint(p[1], 10, 64)
	if e1 != nil || e2 != nil {
		return nil, fmt.Errorf("localapi: invalid lease metadata")
	}
	return &identity{d, i}, nil
}
func writeIdentity(f *os.File, id identity) error {
	if e := f.Truncate(0); e != nil {
		return e
	}
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	if _, e := fmt.Fprintf(f, "%d %d\n", id.dev, id.ino); e != nil {
		return e
	}
	return f.Sync()
}
func removeIfIdentity(path string, want identity) error {
	fi, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	id, ok := validSocket(fi)
	if !ok || id != want {
		return nil
	}
	return os.Remove(path)
}

func (l *Listener) Close() error {
	l.once.Do(func() {
		l.closeErr = errors.Join(l.UnixListener.Close(), removeIfIdentity(l.path, l.id), l.lock.Close())
	})
	return l.closeErr
}
