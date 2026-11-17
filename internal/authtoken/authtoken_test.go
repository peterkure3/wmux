package authtoken

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProvisionAndRepair(t *testing.T) {
	for _, initial := range []string{"missing", "", "truncated", strings.Repeat("a", 64)} {
		t.Run(initial, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			t.Setenv(EnvVar, path)
			if initial != "missing" {
				if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			token, err := LoadOrCreate()
			if err != nil {
				t.Fatal(err)
			}
			if b, err := hex.DecodeString(token); err != nil || len(b) != 32 {
				t.Fatalf("invalid token length/encoding: %v", err)
			}
			second, err := LoadOrCreate()
			if err != nil || second != token {
				t.Fatalf("token changed: %v", err)
			}
			if initial == strings.Repeat("a", 64) && token != initial {
				t.Fatal("existing token replaced")
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("token permissions: %v", info.Mode())
				}
			}
		})
	}
}

func TestTokenHelperProcess(t *testing.T) {
	if os.Getenv("WMUX_TOKEN_TEST_HELPER") != "1" {
		return
	}
	token, err := LoadOrCreate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(token)
	os.Exit(0)
}

func TestConcurrentTokenProvisioningAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")
	t.Setenv(EnvVar, path)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestTokenHelperProcess$")
			// These helpers test process coordination, not coverage collection.
			// Do not let them race with the parent's coverage metadata writes.
			cmd.Env = append(os.Environ(), "WMUX_TOKEN_TEST_HELPER=1", "GOCOVERDIR=")
			var diagnostics bytes.Buffer
			cmd.Stderr = &diagnostics
			b, err := cmd.Output()
			if err != nil {
				t.Errorf("provision: %v: %s", err, &diagnostics)
				return
			}
			if diagnostics.Len() > 0 {
				t.Logf("provisioning subprocess diagnostics: %s", &diagnostics)
			}
			results <- string(b)
		}()
	}
	wg.Wait()
	close(results)
	stored, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for token := range results {
		count++
		if token != stored {
			t.Errorf("processes received different tokens (helper bytes=%d, stored bytes=%d, whitespace=%t)", len(token), len(stored), strings.ContainsAny(token, "\r\n "))
		}
	}
	if count != 8 {
		t.Fatalf("only %d processes succeeded", count)
	}
}

func TestTokenReadErrorIsNotOverwritten(t *testing.T) {
	path := t.TempDir()
	t.Setenv(EnvVar, path)
	if _, err := LoadOrCreate(); err == nil {
		t.Fatal("a directory must not be treated as a missing token")
	}
}
