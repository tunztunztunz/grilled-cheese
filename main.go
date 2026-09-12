// Command grilled-cheese runs the browser UI for a grilling session.
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
	"errors"
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
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

//go:embed demo.json
var demoJSON []byte

func main() {
	log.SetFlags(0)
	log.SetPrefix("grilled-cheese: ")

	if len(os.Args) < 2 {
		usage()
	}
	cmds := map[string]func([]string) error{
		"serve": serve, "new": newCmd, "ask": ask, "wait": waitCmd, "reply": reply,
		"install-service": installService,
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
	fmt.Fprintln(os.Stderr, `usage: grilled-cheese <command> [flags]

  install-service   register a per-user background service and start it
  serve             host the UI and own the session state
  new               clear the session and open the page in a browser
  ask               push a round of questions, read as JSON on stdin
  wait              block until the user answers or the round completes
  reply             respond to one submission, note read from stdin`)
	os.Exit(2)
}

// defaultWorkdir must resolve identically for the user's shell and for a
// sandboxed agent, whose TMPDIR is redirected elsewhere — so it cannot be
// derived from the environment. Only the server writes here; clients just read
// the address, which a sandbox permits.
func defaultWorkdir() string {
	return filepath.Join("/tmp", fmt.Sprintf("grilled-cheese-%d", os.Getuid()))
}

// addrPath is where serve publishes its listen address. Every other subcommand
// dials the port it finds here, so the four readers and the one writer agree on
// the name from one place.
func addrPath(workdir string) string {
	return filepath.Join(workdir, "addr")
}

// The port is fixed rather than ephemeral so a connected device can keep a bookmark across
// restarts, and mdnsName is what that bookmark says. A session bound to
// loopback never claims the name.
const (
	defaultAddr = "127.0.0.1:7331"
	mdnsName    = serviceName + ".local"
)

// lanURL reports the name-based URL for a listen address, empty when that
// address is loopback-only and so unreachable from another device.
func lanURL(addr string) string {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil || a.IP.IsLoopback() {
		return ""
	}
	return fmt.Sprintf("http://%s:%d", mdnsName, a.Port)
}

// publishMDNS answers multicast lookups for name, which is what lets a phone
// reach the session by name with no Bonjour registration on macOS and no avahi
// on Linux. The responder answers with the address of whichever interface the
// query arrived on, keeping a docker bridge or a VPN address out of the reply.
func publishMDNS(name string) error {
	addr4, err := net.ResolveUDPAddr("udp4", mdns.DefaultAddressIPv4)
	if err != nil {
		return err
	}
	addr6, err := net.ResolveUDPAddr("udp6", mdns.DefaultAddressIPv6)
	if err != nil {
		return err
	}
	// 5353 is shared with mDNSResponder and with every other Bonjour speaker on
	// the machine, so these sockets must tolerate company.
	l4, err := net.ListenUDP("udp4", addr4)
	if err != nil {
		return err
	}
	l6, err := net.ListenUDP("udp6", addr6)
	if err != nil {
		l4.Close()
		return err
	}
	if _, err := mdns.NewServer(ipv4.NewPacketConn(l4), ipv6.NewPacketConn(l6), mdns.WithLocalNames(name)); err != nil {
		l4.Close()
		l6.Close()
		return err
	}
	return nil
}

// flags parses the --workdir every subcommand needs. ExitOnError means a bad
// flag exits here, so there is no parse error for callers to handle.
func flags(name string, args []string, extra func(*flag.FlagSet)) string {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("workdir", defaultWorkdir(), "session directory")
	if extra != nil {
		extra(fs)
	}
	fs.Parse(args)
	return *dir
}

// serve hosts the UI and owns the session state until it is signalled. It
// publishes its listen address to workdir/addr, which is how the other
// subcommands find it.
func serve(args []string) error {
	var addr string
	var demo, open bool
	workdir := flags("serve", args, func(fs *flag.FlagSet) {
		fs.StringVar(&addr, "addr", defaultAddr, "listen address")
		fs.BoolVar(&demo, "demo", false, "seed a fixture round for UI work")
		fs.BoolVar(&open, "open", true, "open the page in a browser")
	})

	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return err
	}
	// Two servers sharing a workdir would both write state.json, which the
	// single-writer design rules out. The probe goes through call() rather than
	// a direct dial: a server outside this network namespace answers only over
	// the proxy route, and missing it would clobber a live session's addr file.
	if _, err := call(workdir, "GET", "/state?since=-1", nil); err == nil {
		live, _ := os.ReadFile(addrPath(workdir))
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
	// Clients dial whatever lands in the addr file, and a wildcard bind's own
	// "0.0.0.0:port" would route them off-loopback to reach a server on this very
	// machine. Only the port is theirs to learn.
	tcp := ln.Addr().(*net.TCPAddr)
	local := ln.Addr().String()
	if tcp.IP.IsUnspecified() {
		local = net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
	}
	if err := os.WriteFile(addrPath(workdir), []byte(local), 0o644); err != nil {
		return err
	}
	// Dropping the addr on the way out stops a finished session advertising a
	// port nothing answers on. SIGKILL skips this, which is why call() has to
	// treat an unreachable address as a dead session rather than as a transport
	// failure.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		os.Remove(addrPath(workdir))
		os.Exit(0)
	}()

	page := "http://" + local
	if open {
		show(page)
	} else {
		fmt.Println(page)
	}
	// A name is only worth claiming once the listener answers off-box; a failure
	// to claim it costs nothing, since the address above still reaches the page.
	if url := lanURL(ln.Addr().String()); url != "" {
		if err := publishMDNS(mdnsName); err != nil {
			log.Printf("no mDNS name (%v) — reach this session at port %d on this machine's address", err, tcp.Port)
		} else {
			fmt.Println("on this network: " + url)
		}
	}
	return http.Serve(ln, s.routes())
}

// show prints the page URL and opens it, preferring $BROWSER over the desktop
// default so a configured launcher wins. It does not wait for the browser, and
// reports a failed launch without failing the command: the URL is already out.
func show(page string) {
	fmt.Println(page)
	launcher := os.Getenv("BROWSER")
	if launcher == "" {
		launcher = "xdg-open"
		if runtime.GOOS == "darwin" {
			launcher = "open"
		}
	}
	if err := exec.Command(launcher, page).Start(); err != nil {
		log.Printf("could not open a browser (%v) — visit %s", err, page)
	}
}

// newCmd claims a running server for a fresh grilling: it clears whatever the
// last session left behind and shows the empty board. This is how a session
// starts when the server has been up since login rather than started by hand.
func newCmd(args []string) error {
	workdir := flags("new", args, nil)
	if err := checkFresh(workdir); err != nil {
		return err
	}
	if _, err := call(workdir, "POST", "/new", struct{}{}); err != nil {
		return err
	}
	addr, err := os.ReadFile(addrPath(workdir))
	if err != nil {
		return err
	}
	show("http://" + string(addr))
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
	if !mine.Differs(running) {
		return nil
	}
	return fmt.Errorf("the running server is stale.\n"+
		"  serving: %s (%s)\n"+
		"  current: %s (%s)\n"+
		"Point the service at the current binary with:  %s install-service",
		running.Exe, running.Mod.Format(time.RFC3339),
		mine.Exe, mine.Mod.Format(time.RFC3339), mine.Exe)
}

// ask reads a round as JSON on stdin and pushes it, printing the assigned
// question ids. The round is parsed locally first, so a malformed one fails
// here rather than as an opaque 400.
func ask(args []string) error {
	workdir := flags("ask", args, nil)
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
	workdir := flags("wait", args, nil)
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
	workdir := flags("reply", args, func(fs *flag.FlagSet) {
		fs.StringVar(&id, "id", "", "question id")
		fs.StringVar(&status, "status", "", "open, settled or rejected")
	})
	if id == "" || status == "" {
		return errors.New("--id and --status are required")
	}
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
	addr, err := os.ReadFile(addrPath(workdir))
	if err != nil {
		return nil, fmt.Errorf("no session in %s: is `grilled-cheese serve` running?", workdir)
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	endpoint := "http://" + string(addr) + path

	for _, client := range routes() {
		out, status, err := send(client, method, endpoint, payload)
		if err != nil {
			continue
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
	// A killed server leaves its addr file behind, so the file existing proves
	// nothing about the session being alive or routable from here. The remedy is
	// the same either way, so the message names it in full.
	return nil, fmt.Errorf("cannot reach the session at %s — the server is not running, or not reachable from here.\n"+
		"Start it in a terminal with:  grilled-cheese serve --workdir %s", addr, workdir)
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
func send(client *http.Client, method, endpoint string, payload []byte) ([]byte, int, error) {
	var buf io.Reader
	if payload != nil {
		buf = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, buf)
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
