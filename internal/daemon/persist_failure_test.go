package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

var errDisk = errors.New("injected filesystem failure")

type failingSnapshot struct {
	*os.File
	stage string
}

func (f failingSnapshot) Chmod(mode os.FileMode) error {
	if f.stage == "chmod" {
		return errDisk
	}
	return f.File.Chmod(mode)
}
func (f failingSnapshot) Write(b []byte) (int, error) {
	if f.stage == "write" || f.stage == "short write" {
		n, err := f.File.Write(b[:len(b)/2])
		if err != nil {
			return n, err
		}
		if f.stage == "write" {
			return n, errDisk
		}
		return n, nil
	}
	return f.File.Write(b)
}
func (f failingSnapshot) Sync() error {
	if f.stage == "sync" {
		return errDisk
	}
	return f.File.Sync()
}
func (f failingSnapshot) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return errDisk
	}
	return err
}

func TestSnapshotFailurePreservesPreviousState(t *testing.T) {
	for _, stage := range []string{"chmod", "write", "short write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			old := []byte(`[{"id":"previous"}]`)
			if err := os.WriteFile(path, old, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
			if err != nil {
				t.Fatal(err)
			}
			err = finalizeSnapshot(path, []byte(`[{"id":"replacement"}]`), failingSnapshot{f, stage})
			want := errDisk
			if stage == "short write" {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(old) {
				t.Fatalf("previous snapshot overwritten: %s", got)
			}
			if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
				t.Fatalf("temporary file not removed: %v", err)
			}
		})
	}
}
