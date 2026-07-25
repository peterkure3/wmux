// Package authtoken manages the shared secret that authenticates wmux CLI
// requests to wmuxd.
//
// Why this exists: wmuxd's HTTP API can execute arbitrary commands (POST
// /sessions runs its Command field through bash/wsl.exe). Binding to
// 127.0.0.1 is not a security boundary — any process on the machine can
// reach loopback, and so can any web page the user visits, because a
// cross-origin POST with Content-Type: text/plain is a CORS "simple
// request" that browsers send without a preflight. The daemon decodes the
// body regardless of Content-Type, so the command runs; the attacker never
// needs to read the (opaque) response.
//
// The token blocks non-browser local processes. daemon.browserBlocked
// blocks browsers. Both are needed: a browser can be made to send a
// cross-origin request but cannot be made to read a file, so a
// same-machine attacker who can read ~/.wmux/token is already past a
// different boundary.
package authtoken

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar overrides the token file location, mirroring WMUX_ADDR. Mainly
// for tests and for running several isolated daemons on one machine.
const EnvVar = "WMUX_TOKEN_FILE"

// HeaderName is the request header the token travels in. A custom header
// is deliberate: browsers cannot set one on a cross-origin request without
// triggering a CORS preflight, which the daemon never answers.
const HeaderName = "X-Wmux-Token"

// Path returns the token file location: ~/.wmux/token, alongside the
// state file and logs.
func Path() string {
	if p := os.Getenv(EnvVar); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "wmux-token" // last resort: cwd, same fallback as DefaultStatePath
	}
	return filepath.Join(home, ".wmux", "token")
}

// Load reads the existing token. Returns "" (no error) when the file does
// not exist — callers on the CLI side treat that as "not provisioned yet"
// and let the daemon answer 401 with a useful message, rather than dying
// before they can even report which daemon they failed to reach.
func Load() (string, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// LoadOrCreate returns the existing token, generating and persisting a new
// one if there isn't a usable one yet. Called once at daemon startup.
//
// Note on Windows permissions: the 0o600 mode below is honored on Unix
// but Windows only maps it to the read-only attribute. The real protection
// there is the default ACL on %USERPROFILE%, which already restricts the
// profile directory to its owner, SYSTEM, and Administrators. That is the
// same protection ~/.wmux/state.json has always relied on.
func LoadOrCreate() (string, error) {
	path := Path()

	if tok, err := Load(); err != nil {
		return "", err
	} else if len(tok) >= 32 {
		return tok, nil
	}
	// Falls through when the file is missing, empty, or truncated — a
	// short token is treated as corrupt and replaced rather than used.

	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("could not generate auth token: %w", err)
	}
	tok := hex.EncodeToString(buf[:])

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("could not create token directory: %w", err)
	}
	// Write-then-rename so a concurrent reader never sees a half-written
	// token, matching how state.json is persisted.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("could not write token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("could not finalize token: %w", err)
	}
	return tok, nil
}
