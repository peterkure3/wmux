// wmux notify plugin for mimo (mimocode).
//
// mimo has no command-hook config like Claude Code — integrations are JS
// plugins on its opencode-style event bus. This plugin forwards turn-end
// and error events to `wmux hook run mimo` as stdin JSON shaped like
// Claude Code's hook payload (see profiles/mimo.toml in the wmux repo:
// the profile and this payload are designed together).
//
// Install (global): copy to ~/.config/mimocode/plugin/wmux-notify.js
// Session label: fixed "mimo" (same convention as the Codex wiring's
// --session codex), override with the WMUX_SESSION env var.
//
// Failure posture: everything is fire-and-forget — a missing wmux binary
// or a stopped wmuxd must never break or slow the agent.

export const WmuxNotify = async ({ directory }) => {
  const { spawn } = await import("node:child_process");
  const wmux = process.platform === "win32" ? "wmux.exe" : "wmux";

  const send = (eventName, message) => {
    try {
      const child = spawn(wmux, ["hook", "run", "mimo"], {
        stdio: ["pipe", "ignore", "ignore"],
        detached: true,
      });
      child.on("error", () => {});
      child.stdin.on("error", () => {});
      child.stdin.write(
        JSON.stringify({
          hook_event_name: eventName,
          session_id: process.env.WMUX_SESSION || "mimo",
          cwd: directory,
          message,
        }),
      );
      child.stdin.end();
      child.unref();
    } catch {}
  };

  return {
    event: async ({ event }) => {
      if (event.type === "session.idle") {
        send("session.idle", "mimo finished a turn");
      } else if (event.type === "session.error") {
        send("session.error", "mimo hit an error");
      }
    },
  };
};
