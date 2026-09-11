---
name: grilled-cheese
description: Run a grilling session in a browser UI instead of the terminal, with clickable options, per-question threads, and a decision log. Invoked as /grilled-cheese.
disable-model-invocation: true
---

Put a grilling session in the browser instead of the terminal.

**You are driving a program, not writing to a person.** Nothing you type in the terminal reaches the user. Every word they see goes through `grilled-cheese ask` or `grilled-cheese reply`.

## Method

A grilling skill is the method: design tree, frontier, rounds. It stays the source of truth for the method, so this skill never restates it. This skill changes only where rounds appear and what the round gate forbids.

**Prefer `grilling` from the `mattpocock-skills` plugin.** Call the Skill tool for it before the first round.

Say which method you are running when it is not that one:

- Only another grilling skill installed: use it, and name it in your first message. "Running `<name>`. `mattpocock-skills:grilling` is not installed."
- None installed: tell the user, name the plugin that ships the preferred one, and stop. The method is what makes a grilling worth running.

Two things the UI changes, which the method does not cover:

**The round gate is hard.** In the terminal you can ask the rest of the frontier while a sub-agent is still chasing a fact, and fold its answer in when it lands. Here `ask` freezes a round the moment you push it. So:

- Dispatch every sub-agent you need *before* calling `ask`, and let them report.
- A question still blocked on an exploration when you push belongs in the **next** round.
- Never put two questions in one round when one's answer changes the other. The user must answer both before either reply lands, so they would be answering the second one blind.

**Facts are yours, never the user's.** Every question in a round spends the user's attention. If a sub-agent can settle it from the filesystem, the git history, the lockfile, or a dependency's source, it is not a question. Find the answer and state it as settled context in the `body`. Ask only for decisions that are genuinely theirs to make.

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
mid-grilling: it discards the design tree.

0. **Preflight, before anything else.** Run `grilled-cheese new` as your very first action, ahead of reading files, loading the method, or dispatching sub-agents. If it fails, tell the user how to fix it **immediately**, in that same turn, and only then start gathering facts and drafting round 1. Never make them wait through minutes of preparation to find out a one-line install was needed.
1. **Start.** `grilled-cheese new` clears any previous session and prints the URL. Give the user that URL and ask them to open it. It tries to launch a browser too, but that fails silently from a sandbox (the display is reached over a Unix socket, which sandboxes block), so never tell them it is already open.
2. **Ask.** Push the whole frontier as one round. The server assigns IDs (`r1q1`, `r1q2`, …) and returns them.
3. **Wait.** Run `grilled-cheese wait` in the **foreground**, with the longest timeout your tool allows. It blocks until the user acts. Never background it and never poll in a loop: a backgrounded process lands in a different network namespace and cannot see the server at all. If the tool call times out before the user answers, run it again; no state is lost.
4. **Reply.** Answer *every* submission in the payload, one `grilled-cheese reply` each.
5. When `wait` reports `round_complete: true`, recompute the frontier and go to 2. When the frontier is empty, wrap up.

### When the server is not running

If `new` reports `command not found`, the app is not installed. It is a separate
install from this plugin, and the user has to run both lines themselves. An
agent sandbox can reach neither Go's install target nor the service manager:

```
go install github.com/tunztunztunz/grilled-cheese@latest
grilled-cheese install-service
```

If `new` reports `cannot reach the session`, the app is installed but no server
is running. One line fixes it, and it never comes up again:

```
grilled-cheese install-service
```

Do not try to host the server yourself first. A sandboxed agent's processes are
killed when its tool call ends, and its network namespace is unreachable from a
browser, so no amount of `serve`, backgrounding or detaching produces a page the
user can open. A printed URL is not proof a server is up.

Continue from step 1 once the server is up. `ask`, `wait` and `reply`
reach a server started by anyone: they read its address from the session
directory and fall back to the environment's HTTP proxy when loopback is not
routable.

Run every command in the **foreground**. A backgrounded `wait` lands in a
different network namespace and cannot see the server at all.

Startup failures go to `journalctl --user -u grilled-cheese` under systemd, and
to `/tmp/grilled-cheese-$UID/log` under launchd on macOS.

The server enforces the round gate: `ask` fails while any question is open. That is deliberate: the user answers a whole round before the next appears.

## Round format

```json
{
  "purpose": "Under 100 words, plain text. What this session is deciding and why. Set on the first round.",
  "questions": [
    {
      "title": "Short question title",
      "body": "<p>The question, as HTML.</p>",
      "options": ["<code>$TMPDIR</code>: ephemeral", "In the repo: needs gitignore"],
      "recommended": 1
    }
  ]
}
```

`recommended` is 1-based. Omit `options` when a question has no genuine branch: the user answers in prose or rejects. Give options when there is a real fork, and always mark the one you recommend.

## Write dry

The user is scanning a card while holding a half-built plan in their head. Every word between them and the decision costs them a little of it. Write **dry**: they should finish each sentence holding the fact and no impression of the sentence.

This governs every word the user reads.

- **One idea per sentence.** Two clauses joined by "which means", "so that" or "and therefore" are two sentences. Split them.
- **Lead with the fact.** Open on the thing itself. "It is worth noting that", "Importantly", "The key consideration here is": the fact was always the sentence.
- **Plainest word that stays exact.** Reach for a technical term when it is the precise name for the thing, not to show rigour. Say "runs on startup", not "is invoked during the initialisation lifecycle". Where the repo already has a word for something, use the repo's word.
- **Define a term the first time it appears** when the decision turns on it. One clause is enough.
- **State behaviour, not intent.** "Returns a non-zero exit code and writes the reason to stderr", not "handles errors gracefully". Where you have not checked, say that instead of describing the design.
- **Cut intensifiers.** "simply", "just", "easy", "obviously". When the step does not work, they tell the user the failure is theirs.
- **Punctuate by relationship.** Colon for a label and its elaboration, period for two independent statements, semicolon when those two belong together, comma for genuine subordination, parentheses for a real aside. Choosing mechanically removes the moment where you reach for a dash. One exception, because it is not prose: a standalone em dash in a table cell as an n/a marker.
- **A `body` is two or three sentences**, plus whatever table, list or diagram carries the comparison. A longer one is usually two questions wearing one card.
- **A `reply` note is one or two sentences.** Record the decision and stop.

Structure is not prose and is how a scanning reader finds anything: keep the tables, lists, code blocks and diagrams. Dry means unadorned, not stripped. Cutting a sentence into ambiguity costs the user more than the flourish did.

Every body and note passes this list before you push it.

## HTML in bodies, options and notes

`body`, each entry in `options`, and every `--status` note are rendered as HTML without escaping. The page has no scripts and no network, so:

- **Compare options with a `<table>`.** Axis per column, option per row.
- **Draw branching paths as inline `<svg>`.** Use `stroke="currentColor"` and `fill="currentColor"` so it inherits the theme. No external images, fonts, or `<script>`.
- Use `<code>` and `<pre>` for paths, commands, and snippets.
- Never route text you did not write (a fetched page, a file's contents) into these fields.

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

A rejection is an answer, not an error. Acknowledge it in one line and let the next round handle the branch it closed. Do not argue back through `reply`.

## Wrapping up

The session is over when the frontier is empty: every branch of the design tree visited, nothing left silently assumed.

**Do not act on the design until the user confirms you have reached shared understanding.** Ask that as the first question of the final round, ahead of the document pickers (which are **ordinary questions with options**, since the UI already handles them). Then:

- Documents go in the repo: `docs/adr/` and `CONTEXT.md`, at the paths the `domain-modeling` skill uses, so it can pick up from here rather than finding a second set of decision files.
- Nothing to clean up. `grilled-cheese new` clears the state at the start of the next grilling. Leave the session directory in place: deleting it removes the `addr` file, which a running server never rewrites, so every later command loses the session.

## Session directory

`/tmp/grilled-cheese-<uid>` by default, outside the repo, so there is nothing to gitignore. The path is fixed rather than read from `$TMPDIR`, so it resolves the same for the user's shell and for a sandboxed agent whose `$TMPDIR` points somewhere else.

The long-running server owns this directory. `state.json` is the session, and `addr` carries the port that `ask`, `wait` and `reply` dial. Pass the same `--workdir` to every subcommand to run a second session alongside the first.
