# grilled cheese

Matt Pocock's [grilling method](https://github.com/mattpocock/skills), in a
browser instead of the terminal.

An agent interviews you about a plan one round at a time. Each question lands as
a card with clickable options, a text box for a follow-up, and an outright
reject. Each answer gets a threaded reply, the card tints green or red as it
settles, and a decision log builds as you go. Vim keys throughout.

The server enforces the round gate: no new questions arrive until you have
answered every one on screen.

## Install

Two pieces, installed once each.

**The app**, a single Go binary that serves the UI and owns session state:

```sh
go install github.com/tunztunztunz/grilled-cheese@latest
grilled-cheese install-service
```

`install-service` registers a systemd user service, so a server runs from login
onward. Without systemd, run `grilled-cheese serve` however you keep background
processes.

**The plugin**, the agent-facing half:

```sh
claude plugin marketplace add tunztunztunz/grilled-cheese
claude plugin install grilled-cheese@grilled-cheese
```

Run `/grilled-cheese <what you want stress-tested>` in any project.

## Why the app is separate

An agent cannot host this itself. Its processes are killed when each tool call
ends, and its network namespace is unreachable from a browser. The server has to
be an ordinary background service owned by your login session. The plugin
carries only `SKILL.md`: no binary, no source.

## Commands

| | |
|---|---|
| `install-service` | register a systemd user service and start it |
| `serve` | host the UI and own session state |
| `new` | clear the session and open the page |
| `ask` | push a round of questions, JSON on stdin |
| `wait` | block until the user answers or the round completes |
| `reply` | respond to one submission, HTML note on stdin |

You run `install-service`, or `serve` without systemd. The rest are the agent's.
Session state lives in `/tmp/grilled-cheese-$UID` and survives restarts.

## Building and testing

```sh
go test ./...
go build -o grilled-cheese . && ./grilled-cheese serve --demo
```

`--demo` seeds a fixture round of open, settled and rejected cards, so you can
work on the UI without an agent attached. The page is compiled into the binary
with `go:embed`. Rebuild after editing `index.html`, `app.css` or `app.js`, then
re-run `install-service` to move the running service onto the new binary.
