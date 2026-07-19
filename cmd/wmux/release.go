// The GitHub Releases fetcher for `wmux update --release` — the second
// fetch source beside fetchFromSource, same (stagingDir, version)
// contract: land wmux.exe + wmuxd.exe in a fresh staging dir and report
// the version they carry; cmdUpdate's swap/restart machinery does the
// rest. This is the no-Go-toolchain path: machines that never build from
// source install the binaries the release workflow published.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// releaseRepo is the real GitHub repo. Careful: this deliberately differs
// from go.mod's module path — the repo lives at peterkure3 (with a 3),
// while go.mod says peterkure. Release URLs must use the real repo;
// ldflags -X paths follow go.mod.
const releaseRepo = "peterkure3/wmux"

// releaseClient covers the whole request including body read: an asset is
// a few MB, so minutes of budget means a stall fails rather than hanging
// the update forever.
var releaseClient = &http.Client{Timeout: 5 * time.Minute}

// fetchFromRelease downloads a published release's windows-amd64 archive
// into a staging dir, verifies it against the release's SHA256SUMS, and
// extracts the two binaries. tag is "latest" or an exact tag ("v0.2.0").
// The caller removes stagingDir (returned even on error, once created).
func fetchFromRelease(tag string) (stagingDir, ver string, err error) {
	if tag == "latest" {
		tag, err = latestReleaseTag()
		if err != nil {
			return "", "", err
		}
	}
	asset := fmt.Sprintf("wmux_%s_windows-amd64.zip", tag)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", releaseRepo, tag)

	stagingDir, err = os.MkdirTemp("", "wmux-update-")
	if err != nil {
		return "", "", err
	}

	up.step("downloading " + asset)
	zipPath := filepath.Join(stagingDir, asset)
	if err := downloadFile(base+asset, zipPath); err != nil {
		return stagingDir, "", fmt.Errorf("download %s: %w", asset, err)
	}

	up.step("verifying + extracting")
	sums, err := fetchBody(base + "SHA256SUMS")
	if err != nil {
		return stagingDir, "", fmt.Errorf("download SHA256SUMS: %w", err)
	}
	if err := verifySHA256(zipPath, asset, string(sums)); err != nil {
		return stagingDir, "", err
	}
	if err := extractBinaries(zipPath, stagingDir); err != nil {
		return stagingDir, "", err
	}
	return stagingDir, tag, nil
}

// latestReleaseTag asks the GitHub API which tag "latest" currently
// means. Needed because asset filenames embed the tag, so a fixed
// releases/latest/download URL can't be constructed without it.
func latestReleaseTag() (string, error) {
	body, err := fetchBody(fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", releaseRepo))
	if err != nil {
		return "", fmt.Errorf("query latest release: %w", err)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil || rel.TagName == "" {
		return "", fmt.Errorf("could not parse latest release response from GitHub")
	}
	return rel.TagName, nil
}

// fetchBody GETs a URL and returns its body, treating any non-200 as an
// error (GitHub answers 404 with a body that must not be mistaken for
// content).
func fetchBody(url string) ([]byte, error) {
	resp, err := releaseClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// downloadFile streams a URL to disk, feeding byte progress into the
// update bar's current step label.
func downloadFile(url, dest string) error {
	resp, err := releaseClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	name := filepath.Base(dest)
	var done int64
	buf := make([]byte, 128*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			done += int64(n)
			if resp.ContentLength > 0 {
				up.note(fmt.Sprintf("downloading %s (%d%%)", name, done*100/resp.ContentLength))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}

// verifySHA256 checks a downloaded file against its entry in the
// release's SHA256SUMS ("<hex>  <name>" lines). A missing entry fails as
// hard as a mismatch — silently skipping verification would defeat the
// point of publishing the sums.
func verifySHA256(path, name, sums string) error {
	var want string
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", name)
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("SHA256 mismatch for %s: got %s, want %s — refusing to install a tampered or corrupted archive", name, got, want)
	}
	return nil
}

// extractBinaries pulls exactly wmux.exe and wmuxd.exe out of the release
// zip into outDir. Only the two known names are extracted — the archive
// content is still remote input even after the checksum passed, so no
// path from inside it is ever used to build a destination.
func extractBinaries(zipPath, outDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer r.Close()

	found := map[string]bool{}
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if name != "wmux.exe" && name != "wmuxd.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(filepath.Join(outDir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(dst, rc)
		rc.Close()
		if err := dst.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return cerr
		}
		found[name] = true
	}
	if !found["wmux.exe"] || !found["wmuxd.exe"] {
		return fmt.Errorf("release archive is missing wmux.exe/wmuxd.exe")
	}
	return nil
}
