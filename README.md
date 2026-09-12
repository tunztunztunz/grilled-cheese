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

`install-service` registers a user service — systemd on Linux, launchd on macOS
— so a server runs from login onward. With neither, run `grilled-cheese serve`
however you keep background processes.

## Answering from your phone

By default the server listens on `127.0.0.1:7331` and nothing else on the
network can see it. To answer a grilling from another device on the same wifi,
bind it wider:

```sh
grilled-cheese install-service --addr 0.0.0.0:7331
```

The server then claims `grilled-cheese.local` over mDNS, so the page is at
**http://grilled-cheese.local:7331** from any browser on the network. It answers
the lookups itself — nothing to register in Bonjour on macOS, no avahi needed on
Linux.

Once the page loads, save it as an app icon so you never type the address
again. On iOS, Share → **Add to Home Screen**; on Android, Chrome's ⋮ menu →
**Add to Home screen**. Both launch it full-screen without browser chrome,
which buys back the space the URL bar was taking from the cards.

There is no login. Anyone on the same network can read the session and answer
its questions, so this is for networks you trust. Leave the default loopback
bind on networks you don't. For answering away from home, put the machine on a
VPN such as Tailscale and reach it by its VPN address.

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
| `install-service` | register a user service and start it, `--addr` to bind it |
| `serve` | host the UI and own session state |
| `new` | clear the session and open the page |
| `ask` | push a round of questions, JSON on stdin |
| `wait` | block until the user answers or the round completes |
| `reply` | respond to one submission, HTML note on stdin |

You run `install-service`, or `serve` without a service manager. The rest are the
agent's.
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
