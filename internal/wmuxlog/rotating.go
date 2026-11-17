package wmuxlog

import (
	"os"
	"sync"
)

// rotatingFile rotates between complete records during long daemon runs.
// An individual oversized record is kept intact.
type rotatingFile struct {
	mu    sync.Mutex
	path  string
	file  *os.File
	size  int64
	limit int64
}

func openRotating(path string, limit int64) (*rotatingFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingFile{path: path, file: f, size: info.Size(), limit: limit}, nil
}

func (f *rotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, os.ErrClosed
	}
	if f.size > 0 && f.size+int64(len(p)) > f.limit {
		if err := f.file.Close(); err != nil {
			return 0, err
		}
		f.file = nil
		err := os.Remove(f.path + ".1")
		if err == nil || os.IsNotExist(err) {
			err = os.Rename(f.path, f.path+".1")
		}
		// Reopen after rotation failures so later writes can still succeed.
		next, openErr := openRotating(f.path, f.limit)
		if openErr != nil {
			return 0, openErr
		}
		f.file, f.size = next.file, next.size
		if err != nil {
			return 0, err
		}
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *rotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}
