// Command grill runs the browser UI for a grilling session.
//
// The agent drives it: `serve` hosts the page, `ask` pushes a round of
// questions, `wait` blocks until the user has answered something, and `reply`
// records the agent's response. The serving process owns state.json, so the
// browser and the agent never write it concurrently.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed demo.json
var demoJSON []byte

func main() {
	log.SetFlags(0)
	log.SetPrefix("grill: ")

	if len(os.Args) < 2 {
		usage()
	}
	cmds := map[string]func([]string) error{
		"serve": serve, "new": newCmd, "ask": ask, "wait": waitCmd, "reply": reply,
	}
	run, ok := cmds[os.Args[1]]
	if !ok {
		usage()
	}
	if err := run(os.Args[2:]); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: grilled-cheese <serve|new|ask|wait|reply> [flags]

  serve   host the UI and own the session state
  new     clear the session and open the page in a browser
  ask     push a round of questions, read as JSON on stdin
  wait    block until the user answers or the round completes
  reply   respond to one submission, note read from stdin`)
	os.Exit(2)
}

// flags builds a flag set carrying the --workdir every subcommand needs. The
// default holds the session outside any repo, so nothing has to be gitignored
// and cleanup is a single rm.
func flags(name string, args []string, extra func(*flag.FlagSet)) (string, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("workdir", filepath.Join(os.TempDir(), "grill"), "session directory")
	if extra != nil {
		extra(fs)
	}
	err := fs.Parse(args)
	return *dir, err
}

// serve hosts the UI and owns the session state until it is signalled. It
// publishes its listen address to workdir/addr, which is how the other
// subcommands find it.
func serve(args []string) error {
	var addr string
	var demo, open bool
	workdir, err := flags("serve", args, func(fs *flag.FlagSet) {
		fs.StringVar(&addr, "addr", "127.0.0.1:0", "listen address")
		fs.BoolVar(&demo, "demo", false, "seed a fixture round for UI work")
		fs.BoolVar(&open, "open", true, "open the page in a browser")
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return err
	}
	// Two servers sharing a workdir would both write state.json, which is the
	// one thing the whole design rules out. This probes the same way clients do,
	// since a direct dial cannot see a server hosted outside this namespace and
	// would happily clobber its addr file.
	if _, err := call(workdir, "GET", "/state?since=-1", nil); err == nil {
		live, _ := os.ReadFile(filepath.Join(workdir, "addr"))
		return fmt.Errorf("a session is already serving %s at http://%s", workdir, live)
	}
	s, err := newServer(workdir)
	if err != nil {
		return err
	}
	if demo && len(s.state.Rounds) == 0 {
		var in askPayload
		if err := json.Unmarshal(demoJSON, &in); err != nil {
			return err
		}
		if err := s.seed(in); err != nil {
			return err
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// Clients discover the port here, so a second session only needs its own
	// --workdir to stay out of this one's way.
	addrPath := filepath.Join(workdir, "addr")
	if err := os.WriteFile(addrPath, []byte(ln.Addr().String()), 0o644); err != nil {
		return err
	}
	// Drop the pointer on a graceful stop. A killed server still cannot, which
	// is why call() treats an unreachable address as a dead session.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		os.Remove(addrPath)
		os.Exit(0)
	}()

	url := "http://" + ln.Addr().String()
	fmt.Println(url)
	if open {
		if err := openBrowser(url); err != nil {
			log.Printf("could not open a browser (%v) — visit %s", err, url)
		}
	}
	return http.Serve(ln, s.routes())
}

// openBrowser shows url, preferring $BROWSER over the desktop default so a
// configured launcher wins. It does not wait: the browser outlives this call.
func openBrowser(url string) error {
	launcher := os.Getenv("BROWSER")
	if launcher == "" {
		launcher = "xdg-open"
	}
	return exec.Command(launcher, url).Start()
}

// newCmd claims a running server for a fresh grilling: it clears whatever the
// last session left behind and shows the empty board. This is how a session
// starts when the server has been up since login rather than started by hand.
func newCmd(args []string) error {
	workdir, err := flags("new", args, nil)
	if err != nil {
		return err
	}
	if err := checkFresh(workdir); err != nil {
		return err
	}
	if _, err := call(workdir, "POST", "/new", struct{}{}); err != nil {
		return err
	}
	addr, err := os.ReadFile(filepath.Join(workdir, "addr"))
	if err != nil {
		return err
	}
	url := "http://" + string(addr)
	fmt.Println(url)
	if err := openBrowser(url); err != nil {
		log.Printf("could not open a browser (%v) — visit %s", err, url)
	}
	return nil
}

// checkFresh fails when the running server is older code than this binary. A
// service outlives every rebuild and every plugin upgrade, and would otherwise
// go on serving the previous version with nothing to show that it had.
func checkFresh(workdir string) error {
	out, err := call(workdir, "GET", "/version", nil)
	if err != nil {
		return err
	}
	var running build
	if err := json.Unmarshal(out, &running); err != nil {
		return err
	}
	mine := startedAs
	if !mine.Stale(running) {
		return nil
	}
	installer := filepath.Join(filepath.Dir(mine.Exe), "..", "..", "install.sh")
	return fmt.Errorf("the running server is stale.\n"+
		"  serving: %s (%s)\n"+
		"  current: %s (%s)\n"+
		"Restart it with:  systemctl --user restart grilled-cheese\n"+
		"or reinstall:     %s",
		running.Exe, running.Mod.Format(time.RFC3339),
		mine.Exe, mine.Mod.Format(time.RFC3339),
		filepath.Clean(installer))
}

// ask reads a round as JSON on stdin and pushes it, printing the assigned
// question ids. The round is parsed locally first, so a malformed one fails
// here rather than as an opaque 400.
func ask(args []string) error {
	workdir, err := flags("ask", args, nil)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var in askPayload
	if err := json.Unmarshal(body, &in); err != nil {
		return fmt.Errorf("parsing round: %w", err)
	}
	out, err := call(workdir, "POST", "/ask", in)
	if err != nil {
		return err
	}
	os.Stdout.Write(out)
	return nil
}

// waitCmd blocks until the user answers or the round completes, prints the
// payload, and exits. It must run in the foreground: a backgrounded caller can
// land in a network namespace with no route to the server.
func waitCmd(args []string) error {
	workdir, err := flags("wait", args, nil)
	if err != nil {
		return err
	}
	// The server returns 204 when its long poll expires with nothing to do;
	// reconnecting keeps the block invisible to the caller.
	for {
		out, err := call(workdir, "GET", "/wait", nil)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(out)) > 0 {
			os.Stdout.Write(out)
			return nil
		}
	}
}

// reply records the agent's response to one submission. The note is read from
// stdin because it is HTML, which does not survive a shell argument intact.
func reply(args []string) error {
	var id, status string
	workdir, err := flags("reply", args, func(fs *flag.FlagSet) {
		fs.StringVar(&id, "id", "", "question id")
		fs.StringVar(&status, "status", "", "open, settled or rejected")
	})
	if err != nil {
		return err
	}
	if id == "" || status == "" {
		return fmt.Errorf("--id and --status are required")
	}
	// The note is HTML, which does not survive a shell argument intact.
	note, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	_, err = call(workdir, "POST", "/reply", map[string]any{
		"id": id, "status": status, "note": string(note),
	})
	return err
}

// call sends a request to the session's server, resolving its address from
// workdir. Any status at or above 400 returns as an error carrying the server's
// message, so a rejected mutation reaches the caller's exit code.
func call(workdir, method, path string, body any) ([]byte, error) {
	addr, err := os.ReadFile(filepath.Join(workdir, "addr"))
	if err != nil {
		return nil, fmt.Errorf("no session in %s: is `grilled-cheese serve` running?", workdir)
	}
	// A killed server leaves its addr file behind, so the file existing proves
	// nothing about the session being alive or routable from here. The remedy
	// is the same either way, so the message names it in full.
	unreachable := func() error {
		return fmt.Errorf("cannot reach the session at %s — the server is not running, or not reachable from here.\n"+
			"Start it in a terminal with:  grilled-cheese serve --workdir %s", addr, workdir)
	}

	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	url := "http://" + string(addr) + path

	for _, client := range routes() {
		out, status, err := send(client, method, url, payload)
		if err != nil {
			continue // this route cannot reach the server; try the next
		}
		// A proxy that cannot reach the server answers 502 or 504 on its own
		// behalf. The session server emits neither, so these mean the route
		// failed rather than the request.
		if status == http.StatusBadGateway || status == http.StatusGatewayTimeout {
			continue
		}
		reachedBy = client
		if status >= 400 {
			return nil, fmt.Errorf("%s", bytes.TrimSpace(out))
		}
		return out, nil
	}
	return nil, unreachable()
}

// reachedBy remembers the route that worked, so a `wait` loop does not retry a
// dead one on every reconnect.
var reachedBy *http.Client

// routes lists the ways to reach the server, direct first. A sandboxed agent
// cannot open the host's loopback itself, but the environment's HTTP proxy can
// — and NO_PROXY normally lists 127.0.0.1, so Go skips that route unless it is
// named explicitly.
func routes() []*http.Client {
	if reachedBy != nil {
		return []*http.Client{reachedBy}
	}
	out := []*http.Client{http.DefaultClient}
	if p, err := url.Parse(os.Getenv("HTTP_PROXY")); err == nil && p.Host != "" {
		out = append(out, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(p)}})
	}
	return out
}

// send reports the transport error separately from the HTTP status, so only an
// unreachable route falls through to the next one.
func send(client *http.Client, method, url string, payload []byte) ([]byte, int, error) {
	var buf io.Reader
	if payload != nil {
		buf = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, buf)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	return out, resp.StatusCode, err
}
