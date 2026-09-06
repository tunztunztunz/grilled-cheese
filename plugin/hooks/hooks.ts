import type { Register } from "claude-code";

// The window a launched server may sit idle, and how often this session says it
// is still here. Three beats fit inside the window, so a missed one — a slow
// host, a timer that fired late — does not cost a live session its server.
const IDLE = "30m";
const BEAT = 10 * 60_000;

// Installs the server if it is missing and starts it, so the user runs neither
// command. Both are things an agent cannot do from a tool call: its processes
// die with the call and its network namespace is unreachable from a browser.
// A hook runs outside that sandbox, which is the whole reason this exists.
//
// `ask`, `wait` and `reply` stay on the CLI, so the plugin still works when
// function hooks are off — they are gated by CLAUDE_CODE_ENABLE_FUNCTION_HOOKS
// and by a rollout flag, so this cannot yet be the only path.
export const register: Register = (on) => {
  on("session.start", async ($, e, next) => {
    const sh = (script: string, timeoutMs = 10_000) =>
      $.process.run(["sh", "-c", script], { timeoutMs });

    // The binary has to be resolved before launching, never inferred from the
    // launch: `setsid -f` forks and exits 0 whether or not its command exists,
    // so a missing server would otherwise read as a running one.
    let exe = (await sh("command -v grilled-cheese")).stdout.trim();

    if (!exe) {
      $.ui.status("grilled cheese: building the server (first run only)…");
      // finally, not a following statement: a throw here would otherwise leave
      // the status line claiming a build that is no longer running.
      const built = await sh("go install github.com/tunztunztunz/grilled-cheese@latest", 300_000)
        .finally(() => $.ui.status(undefined));
      if (built.exitCode !== 0) {
        $.ui.log(`grilled cheese: install failed — ${built.stderr.trim() || "is Go on PATH?"}`);
        return next(e);
      }
      // Where go install put it. PATH may not have it yet: a version manager
      // regenerates its shims on its own schedule, not on go install. GOBIN is
      // unset on most machines, and the install directory is then GOPATH/bin —
      // falling back to the bare name would just re-resolve the PATH that has
      // already been shown not to have it.
      const dir = (await sh('b=$(go env GOBIN); echo "${b:-$(go env GOPATH)/bin}"')).stdout.trim();
      exe = `${dir}/grilled-cheese`;
    }

    // ponytail: setsid detaches, so this is Linux/BSD. $.process.run always
    // waits for its child and kills it at timeoutMs, so the server has to
    // leave its process group to outlive the call. macOS needs another way.
    //
    // --idle is the capability flag. Asking for a window is a promise to come
    // back for it, which only a launcher can make, so `serve` never idles out
    // for a user at a prompt or for the systemd unit.
    const start = () =>
      $.process.run(["setsid", "-f", exe, "serve", "--open=false", "--idle", IDLE], {
        timeoutMs: 15_000,
      });
    await start();

    // `serve` refuses a workdir that is already served, so a second start is a
    // no-op. Since the launch cannot report failure, ask the process table.
    //
    // The pattern is anchored to the start of the command line. Unanchored, it
    // also matches every process that merely mentions it — including the very
    // `sh -c` running the pgrep, which made this check report success always.
    await $.clock.sleep(600);
    const alive = await sh("pgrep -f '^[^ ]*grilled-cheese serve'", 5_000);
    if (alive.exitCode !== 0) $.ui.log(`grilled cheese: server is not running (${exe})`);

    // This build raises no session.end, so a stopped heartbeat is the only
    // end-of-session signal there is: these timers die with the session, and
    // the window then runs out on a server nobody can reach any more.
    //
    // Restarting is the heartbeat rather than a second mechanism — `serve`
    // probes over HTTP before refusing an occupied workdir, so the probe is
    // itself the request that pushes the window out, and the same call revives
    // a server that has genuinely gone.
    $.clock.every(BEAT, start);
    return next(e);
  });
};
