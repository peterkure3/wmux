package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/peterkure/wmux/internal/proto"
	"github.com/peterkure/wmux/internal/version"
)

// defaultRepo is stamped by a previous `wmux update` run via
// -ldflags "-X main.defaultRepo=<path>" (main packages link their symbols
// as plain "main", not the import path — verified: the full-path form is
// silently ignored), so after one bootstrap run with --repo the setting
// is self-perpetuating.
var defaultRepo = ""

// cmdUpdate rebuilds wmux + wmuxd from the source repo and swaps the
// binaries this executable is running from — the automated version of the
// manual dance in MANUAL.md (stop daemon, go build, copy, restart).
//
// Windows lets a running .exe be *renamed* (open handles follow the
// rename), just not overwritten — so the running wmux.exe becomes
// wmux.exe.old and a fresh copy lands under the original name. Live
// pane/attach sessions keep executing the renamed old binary untouched;
// they pick up the new one when reopened. wmuxd holds its own .exe locked,
// so the daemon is stopped via POST /shutdown first and restarted
// (detached) after the swap, iff it was running before.
//
// A future GitHub Releases source slots in beside fetchFromSource — same
// (stagingDir, version) contract, different fetch. Note the repo lives at
// github.com/peterkure3/wmux (with a 3) while go.mod says peterkure/wmux;
// release URLs must use the real repo, ldflags -X paths follow go.mod.
func cmdUpdate(args []string) {
	fs := newFlagSet("update")
	repoFlag := fs.String("repo", "", "path to the wmux source repo (falls back to WMUX_REPO, then the path stamped into this binary)")
	noPull := fs.Bool("no-pull", false, "build the repo as-is without git pull")
	killSurfaces := fs.Bool("kill-surfaces", false, "proceed even if live surfaces exist — the wmuxd restart kills them")
	fs.Parse(args)

	if runtime.GOOS != "windows" {
		fatalUpdate("wmux update is Windows-only for now — inside WSL, update the linux binary manually (see MANUAL.md)")
	}

	exe, err := os.Executable()
	if err != nil {
		fatalUpdate("could not resolve wmux.exe's own path: %v", err)
	}
	deployDir := filepath.Dir(exe)

	// Collect .old leftovers from the previous update. Best-effort: a
	// long-lived session may still hold wmux.exe.old locked — harmless,
	// the next update tries again.
	cleanupOld(deployDir)

	repo, err := resolveRepo(*repoFlag)
	if err != nil {
		fatalUpdate("%v", err)
	}

	stagingDir, newVer, err := fetchFromSource(repo, !*noPull)
	if stagingDir != "" {
		defer os.RemoveAll(stagingDir)
	}
	if err != nil {
		fatalUpdate("%v", err)
	}

	oldVer := version.String()
	if newVer == oldVer && !strings.Contains(newVer, "dirty") {
		fmt.Printf("already up to date (%s)\n", oldVer)
		return
	}

	wasRunning := daemonRunning()
	if wasRunning {
		sessions := listRunningSessions()
		var surfaces, others []proto.SessionInfo
		for _, s := range sessions {
			if s.Surface {
				surfaces = append(surfaces, s)
			} else {
				others = append(others, s)
			}
		}

		// A pane/attach session survives the update untouched (it runs its
		// own process; only its metadata tracking blips) — a surface does
		// not: its ConPTY and screen state live inside the wmuxd process
		// this update is about to restart. Refuse rather than silently
		// killing agent sessions mid-turn.
		if len(surfaces) > 0 && !*killSurfaces {
			fmt.Fprintf(os.Stderr, "wmux update: %d live surface(s) would be killed by the wmuxd restart (a surface's ConPTY dies with its daemon):\n", len(surfaces))
			for _, s := range surfaces {
				fmt.Fprintf(os.Stderr, "  %s (cwd=%s)\n", s.ID, s.Cwd)
			}
			fatalUpdate("finish them first ('exit' inside, or wmux close --id ID), or rerun with --kill-surfaces — nothing was changed")
		}
		if len(surfaces) > 0 {
			fmt.Fprintf(os.Stderr, "warning: killing %d live surface(s) (--kill-surfaces):\n", len(surfaces))
			for _, s := range surfaces {
				fmt.Fprintf(os.Stderr, "  %s (cwd=%s)\n", s.ID, s.Cwd)
			}
		}
		if len(others) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d session(s) keep running the old binary until reopened:\n", len(others))
			for _, s := range others {
				fmt.Fprintf(os.Stderr, "  %s (cwd=%s)\n", s.ID, s.Cwd)
			}
		}
		if err := stopDaemon(); err != nil {
			fatalUpdate("%v — nothing was changed", err)
		}
	}

	wmuxDest := filepath.Join(deployDir, "wmux.exe")
	wmuxdDest := filepath.Join(deployDir, "wmuxd.exe")

	wmuxdAside, err := swapBinary(filepath.Join(stagingDir, "wmuxd.exe"), wmuxdDest)
	if err != nil {
		restoreBinary(wmuxdDest, wmuxdAside)
		restartOldDaemonAfterFailure(wasRunning, wmuxdDest)
		fatalUpdate("could not install wmuxd.exe: %v", err)
	}
	wmuxAside, err := swapBinary(filepath.Join(stagingDir, "wmux.exe"), wmuxDest)
	if err != nil {
		// Roll BOTH back — never leave a version-skewed wmux/wmuxd pair.
		restoreBinary(wmuxDest, wmuxAside)
		restoreBinary(wmuxdDest, wmuxdAside)
		restartOldDaemonAfterFailure(wasRunning, wmuxdDest)
		fatalUpdate("could not install wmux.exe: %v", err)
	}

	// The old daemon has exited, so its aside copy usually deletes fine
	// now. wmux.exe's aside cannot go yet — this very process (or a
	// long-lived pane like the sidebar) may run from it; the next update's
	// cleanupOld collects it.
	if wmuxdAside != "" {
		os.Remove(wmuxdAside)
	}

	if wasRunning {
		if err := startDaemonDetached(wmuxdDest); err != nil {
			fatalUpdate("update succeeded (%s -> %s) but wmuxd did not restart: %v — start %s manually", oldVer, newVer, err, wmuxdDest)
		}
		if !pollHealthz(5*time.Second, true) {
			fatalUpdate("update succeeded (%s -> %s) but wmuxd is not answering /healthz — check ~/.wmux/wmuxd.log", oldVer, newVer)
		}
	}

	fmt.Printf("updated wmux: %s -> %s\n", oldVer, newVer)
}

func cmdVersion(args []string) {
	fmt.Println(version.String())
}

func fatalUpdate(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "wmux update: "+format+"\n", a...)
	os.Exit(1)
}

// resolveRepo picks the source repo location: --repo flag, then the
// WMUX_REPO env var, then the path stamped into this binary by the update
// that built it.
func resolveRepo(flagVal string) (string, error) {
	repo := flagVal
	if repo == "" {
		repo = os.Getenv("WMUX_REPO")
	}
	if repo == "" {
		repo = defaultRepo
	}
	if repo == "" {
		return "", fmt.Errorf("no source repo configured — pass --repo D:\\path\\to\\wmux or set WMUX_REPO")
	}
	// Sanity check: this must actually be the wmux repo before we build
	// and install whatever it contains.
	gomod, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("%s does not look like the wmux source repo: %v", repo, err)
	}
	if !strings.Contains(string(gomod), "module github.com/peterkure/wmux") {
		return "", fmt.Errorf("%s\\go.mod is not module github.com/peterkure/wmux", repo)
	}
	return repo, nil
}

// fetchFromSource is the build-from-source fetcher: optionally pull, then
// build both binaries into a fresh staging dir. A future GitHub Releases
// fetcher replaces this call with the same (stagingDir, version) contract.
// The caller removes stagingDir (returned even on error, once created).
func fetchFromSource(repo string, pull bool) (stagingDir, ver string, err error) {
	if pull {
		out, err := exec.Command("git", "-C", repo, "pull", "--ff-only").CombinedOutput()
		if err != nil {
			return "", "", fmt.Errorf("git pull failed (use --no-pull to build as-is):\n%s", strings.TrimSpace(string(out)))
		}
	}

	ver, err = gitDescribe(repo)
	if err != nil {
		return "", "", err
	}

	stagingDir, err = os.MkdirTemp("", "wmux-update-")
	if err != nil {
		return "", "", err
	}
	if err := buildBinaries(repo, stagingDir, ver); err != nil {
		return stagingDir, "", err
	}
	return stagingDir, ver, nil
}

func gitDescribe(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "", fmt.Errorf("git describe failed in %s: %v", repo, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// buildBinaries builds wmux.exe and wmuxd.exe into outDir, stamping the
// version (and, for wmux, the repo path so the next update finds its
// source without --repo). ldflags -X values are single-quoted because the
// linker splits the flag string on spaces and repo paths may contain them.
func buildBinaries(repo, outDir, ver string) error {
	builds := []struct{ out, pkg, ldflags string }{
		{"wmux.exe", "./cmd/wmux", fmt.Sprintf(
			"-X 'github.com/peterkure/wmux/internal/version.Version=%s' -X 'main.defaultRepo=%s'", ver, repo)},
		{"wmuxd.exe", "./cmd/wmuxd", fmt.Sprintf(
			"-X 'github.com/peterkure/wmux/internal/version.Version=%s'", ver)},
	}
	for _, b := range builds {
		cmd := exec.Command("go", "build", "-ldflags", b.ldflags, "-o", filepath.Join(outDir, b.out), b.pkg)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build %s failed (is Go installed and on PATH?): %v\n%s", b.pkg, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func daemonRunning() bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(daemonAddr + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func listRunningSessions() []proto.SessionInfo {
	resp, err := http.Get(daemonAddr + "/sessions")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var sessions []proto.SessionInfo
	json.NewDecoder(resp.Body).Decode(&sessions)
	running := sessions[:0]
	for _, s := range sessions {
		if s.Running {
			running = append(running, s)
		}
	}
	return running
}

// stopDaemon asks wmuxd to exit (releasing its .exe file lock) and waits
// for the port to actually go quiet.
func stopDaemon() error {
	resp, err := http.Post(daemonAddr+"/shutdown", "application/json", nil)
	if err != nil {
		return fmt.Errorf("could not reach wmuxd to stop it: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Bootstrap path: the running daemon predates the /shutdown
		// endpoint. Kill it hard — state.json is persisted after every
		// mutation, so nothing is lost.
		fmt.Fprintln(os.Stderr, "note: running wmuxd predates /shutdown; stopping it via taskkill")
		if out, err := exec.Command("taskkill", "/F", "/IM", "wmuxd.exe").CombinedOutput(); err != nil {
			return fmt.Errorf("taskkill wmuxd.exe failed: %v\n%s", err, strings.TrimSpace(string(out)))
		}
	}
	if !pollHealthz(5*time.Second, false) {
		return fmt.Errorf("wmuxd did not stop within 5s")
	}
	return nil
}

// pollHealthz waits until the daemon is up (wantUp) or gone (!wantUp).
func pollHealthz(timeout time.Duration, wantUp bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemonRunning() == wantUp {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return daemonRunning() == wantUp
}

// swapBinary replaces dest with newPath's content: rename dest aside
// (legal even while dest is a running .exe — same directory, so same
// volume), then copy the new file in (copy, not rename: the staging temp
// dir may be on a different volume than the deploy dir). The aside name
// is unique per run (dest.old.<unix-nanos>) rather than a fixed dest.old:
// a long-lived process still executing a previous update's aside copy
// (the sidebar pane, or this very update process) keeps that file locked,
// and renaming onto a locked existing file fails with access denied —
// which is exactly how a fixed name made the whole update abort once the
// sidebar existed. Returns the aside path actually used ("" if dest
// didn't exist), which restoreBinary needs to undo precisely this run's
// move and nothing else.
func swapBinary(newPath, dest string) (asidePath string, err error) {
	if _, statErr := os.Stat(dest); statErr == nil {
		asidePath = fmt.Sprintf("%s.old.%d", dest, time.Now().UnixNano())
		if err := os.Rename(dest, asidePath); err != nil {
			return "", fmt.Errorf("could not move old binary aside: %w", err)
		}
	}
	err = copyFile(newPath, dest)
	if err != nil {
		// Defender sometimes briefly holds freshly written exes; one short
		// retry is cheap insurance.
		time.Sleep(500 * time.Millisecond)
		err = copyFile(newPath, dest)
	}
	return asidePath, err
}

// restoreBinary undoes a swapBinary: drop any half-copied dest and put
// this run's aside copy back. asidePath == "" means swapBinary never
// moved anything aside — dest is whatever it was before, so touching it
// would only destroy a good binary (removing dest unconditionally is
// exactly what nearly deleted a healthy wmux.exe once). If even the
// restore rename fails there is no automated way out — tell the user the
// exact manual fix.
func restoreBinary(dest, asidePath string) {
	if asidePath == "" {
		return // nothing was moved aside; dest was never touched
	}
	os.Remove(dest)
	if err := os.Rename(asidePath, dest); err != nil {
		fmt.Fprintf(os.Stderr, "wmux update: could not restore %s: %v\n  fix manually: rename %s back to %s\n", dest, err, asidePath, dest)
	}
}

func restartOldDaemonAfterFailure(wasRunning bool, wmuxdPath string) {
	if !wasRunning {
		return
	}
	if err := startDaemonDetached(wmuxdPath); err != nil {
		fmt.Fprintf(os.Stderr, "wmux update: could not restart wmuxd after failed update: %v — start %s manually\n", err, wmuxdPath)
	}
}

// cleanupOld collects aside copies left by previous updates — both the
// current unique names (wmux.exe.old.<nanos>) and the legacy fixed .old
// names. Best-effort: a long-lived process (sidebar pane, an old attach
// session) may still hold one locked; it stays until a later update runs
// after that process is gone.
func cleanupOld(dir string) {
	for _, pattern := range []string{"wmux.exe.old*", "wmuxd.exe.old*"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
