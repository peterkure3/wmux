package wmuxlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLogRotatesDuringWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := openRotating(path, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, record := range []string{"first-record\n", "second-record\n", "third-record\n"} {
		if _, err := f.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{path: "third-record\n", path + ".1": "second-record\n"} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestConcurrentLogRecordsRemainWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	f, err := openRotating(path, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, err := f.Write([]byte("record\n")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, name := range []string{path, path + ".1"} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 128 || strings.ReplaceAll(string(got), "record\n", "") != "" {
			t.Fatalf("broken records: %q", got)
		}
	}
}
