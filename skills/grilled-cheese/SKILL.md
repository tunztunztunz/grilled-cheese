---
name: grilled-cheese
description: Run a grilling session in a browser UI instead of the terminal, with clickable options, per-question threads, and a decision log. Invoked as /grilled-cheese.
disable-model-invocation: true
---

Put a grilling session in the browser instead of the terminal.

**You are driving a program, not writing to a person.** Nothing you type in the terminal reaches the user. Every word they see goes through `grilled-cheese ask` or `grilled-cheese reply`.

## Method

**Call the Skill tool for `grilling` before the first round.** That skill is the method — design tree, frontier, rounds — and it stays the source of truth for it, so this one never drifts from it. This skill only changes where rounds appear and what the round gate forbids. (`grilling` ships in the `mattpocock-skills` plugin; without it installed there is no method to run and you should say so rather than improvise one.)

Two things the UI changes, which `grilling` does not cover:

**The round gate is hard.** In the terminal you can ask the rest of the frontier while a sub-agent is still chasing a fact, and fold its answer in when it lands. Here `ask` freezes a round the moment you push it. So:

- Dispatch every sub-agent you need *before* calling `ask`, and let them report.
- A question still blocked on an exploration when you push belongs in the **next** round.
- Never put two questions in one round when one's answer changes the other. The user must answer both before either reply lands, so they would be answering the second one blind.

**Facts are yours, never the user's.** Every question in a round spends the user's attention. If a sub-agent can settle it from the filesystem, the git history, the lockfile, or a dependency's source, it is not a question — find the answer and state it as settled context in the `body`. Ask only for decisions that are genuinely theirs to make.

The frontier is every decision whose prerequisites are already settled. Ask all of it, never more.

## The loop

`grilled-cheese` is a separate program the user installs; this plugin does not
ship it. Every command below runs it from `PATH`.

```
grilled-cheese new                                # claim the running server, open the page
grilled-cheese ask   < round.json                 # push a round; prints the assigned question IDs
grilled-cheese wait                               # blocks until the user has answered
grilled-cheese reply --id r1q2 --status settled   # note read from stdin
```

The server is expected to be running already, as a user service started at
login. `new` claims it: it clears whatever the last grilling left behind and
opens the browser. Run it once, at the start of a session, and never
mid-grilling — it discards the design tree.

0. **Preflight, before anything else.** Run `grilled-cheese new` as your very first action — ahead of reading files, loading `grilling`, or dispatching sub-agents. If it fails, tell the user how to fix it **immediately**, in that same turn, and only then start gathering facts and drafting round 1. Never make them wait through minutes of preparation to find out a one-line install was needed.
1. **Start.** `grilled-cheese new` clears any previous session, opens the user's browser and prints the URL.
2. **Ask.** Push the whole frontier as one round. The server assigns IDs (`r1q1`, `r1q2`, …) and returns them.
3. **Wait.** Run `grilled-cheese wait` in the **foreground**, with the longest timeout your tool allows. It blocks until the user acts. Never background it and never poll in a loop: a backgrounded process lands in a different network namespace and cannot see the server at all. If the tool call times out before the user answers, simply run it again — no state is lost.
4. **Reply.** Answer *every* submission in the payload, one `grilled-cheese reply` each.
5. When `wait` reports `round_complete: true`, recompute the frontier and go to 2. When the frontier is empty, wrap up.

### When the server is not running

If `new` reports `command not found`, the app is not installed on this machine.
It is a separate install from this plugin:

```
go install github.com/tunztunztunz/grilled-cheese@latest
```

If `new` reports `cannot reach the session`, it is installed but no server is
running. Run the installer **once**.

It sits two directories above this skill, so build the path from the base
directory you were given when this skill loaded — never guess it, and never
assume a checkout of the source exists on this machine:

```
<skill directory>/../../install.sh
```

Give the user the resolved absolute path, not the template.

It registers a systemd user service, so the server is up from login onward and
this never comes up again. Do not try to host the server yourself first: a
sandboxed agent's processes are killed when its tool call ends, and its network
namespace is unreachable from a browser, so no amount of `serve`, backgrounding
or detaching produces a page the user can open. A printed URL is not proof a
server is up.

The installer is different, because it does not need to survive. It hands the
service to systemd and exits; systemd owns the process from then on.

If your harness sandboxes commands, run the installer with its escape hatch —
that surfaces a permission prompt, which is far less work for the user than
opening a terminal. Say plainly what it does: registers a background service
that serves the grilling UI on localhost.

If the escape hatch is unavailable or the user declines, ask them to run that
same command themselves. On a machine without systemd the installer prints the
one command to leave running in a terminal instead.

Continue from step 1 once the server is up. `ask`, `wait` and `reply`
reach a server started by anyone: they read its address from the session
directory and fall back to the environment's HTTP proxy when loopback is not
routable.

Run every command in the **foreground**. A backgrounded `wait` lands in a
different network namespace and cannot see the server at all.

When the server runs under systemd, startup failures go to
`journalctl --user -u grilled-cheese`.

The server enforces the round gate: `ask` fails while any question is open. That is deliberate — the user answers a whole round before the next appears.

## Round format

```json
{
  "purpose": "Under 100 words, plain text. What this session is deciding and why. Set on the first round.",
  "questions": [
    {
      "title": "Short question title",
      "body": "<p>The question, as HTML.</p>",
      "options": ["<code>$TMPDIR</code> — ephemeral", "In the repo — needs gitignore"],
      "recommended": 1
    }
  ]
}
```

`recommended` is 1-based. Omit `options` when a question has no genuine branch — the user answers in prose or rejects. Give options when there is a real fork, and always mark the one you recommend.

## Writing question bodies

`body`, each entry in `options`, and every `--status` note are rendered as HTML without escaping. The page has no scripts and no network, so:

- **Compare options with a `<table>`.** Axis per column, option per row.
- **Draw branching paths as inline `<svg>`.** Use `stroke="currentColor"` and `fill="currentColor"` so it inherits the theme. No external images, fonts, or `<script>`.
- Use `<code>` and `<pre>` for paths, commands, and snippets.
- Never route text you did not write — a fetched page, a file's contents — into these fields.

## Replying

One reply per submission, note on stdin:

```
printf '<p>Recorded. That keeps the repo clean.</p>' | grilled-cheese reply --id r1q2 --status settled
```

| `--status` | Meaning | Card |
|---|---|---|
| `open` | You asked a clarifying question back; the user answers again | stays live |
| `settled` | The decision is recorded | green |
| `rejected` | The user turned the proposal down; record it and re-plan that branch | red |

A rejection is an answer, not an error. Acknowledge it in one line and let the next round handle the branch it closed — do not argue back through `reply`.

## Wrapping up

The session is over when the frontier is empty: every branch of the design tree visited, nothing left silently assumed.

**Do not act on the design until the user confirms you have reached shared understanding.** Ask that as the first question of the final round, ahead of the cleanup and document pickers — which are **just ordinary questions with options**, since the UI already handles them. Then:

- Documents go in the repo — `docs/adr/` and `CONTEXT.md`, at the paths the `domain-modeling` skill uses, so it can pick up from here rather than finding a second set of decision files.
- Session scratch is disposable: `rm -rf "${TMPDIR:-/tmp}/grill"` once the user confirms.

## Session directory

Everything lives in `${TMPDIR:-/tmp}/grill` — outside the repo, so there is nothing to gitignore. Pass the same `--workdir` to every subcommand to run a second session, or to put one somewhere it survives a reboot.
