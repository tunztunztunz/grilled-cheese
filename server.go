package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// assets holds the browser files, served from the binary so there is no static
// directory to locate at runtime.
//
//go:embed assets
var assets embed.FS

// ui strips the assets/ prefix, so the page loads app.css and icon.png from /
// as it does when opened straight off disk. fs.Sub only fails on a malformed
// name, and this one is a constant.
var ui, _ = fs.Sub(assets, "assets")

// longPoll caps a blocked /state or /wait request. Browsers and the agent both
// reconnect immediately, so this only has to stay under their idle timeouts.
const longPoll = 60 * time.Second

// waitInstructions rides in every /wait payload. Repeating the protocol each
// turn is what stops a long session drifting back into answering as prose.
const waitInstructions = `Reply to every submission with: grilled-cheese reply --id <id> --status open|settled|rejected (HTML note on stdin). ` +
	`Use status open to ask a clarifying question, settled to record the decision, rejected to record that the proposal was turned down. ` +
	`Do not write prose to the terminal. When round_complete is true, push the next round with: grilled-cheese ask < round.json`

// server owns the session. It is the only writer to state.json, so the browser
// and the agent both mutate through its handlers rather than the file.
type server struct {
	mu      sync.Mutex
	state   State
	version int
	changed chan struct{} // closed and replaced on every mutation
	path    string
}

// newServer opens the session in workdir, resuming state.json when it exists.
// A missing file is a new session, not an error.
func newServer(workdir string) (*server, error) {
	s := &server{changed: make(chan struct{}), path: filepath.Join(workdir, "state.json")}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.state)
}

// mutate applies fn under the lock, persists, and wakes every waiter. All
// writes go through it so the on-disk state can never lag a broadcast.
func (s *server) mutate(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, b, 0o644); err != nil {
		return err
	}
	s.version++
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}

// waitFor blocks until the state moves past since or longPoll elapses, then
// reports the version it reached. Any mutation satisfies any waiter, so a
// single wakeup is always enough.
func (s *server) waitFor(ctx context.Context, since int) int {
	s.mu.Lock()
	if s.version > since {
		defer s.mu.Unlock()
		return s.version
	}
	ch := s.changed
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, longPoll)
	defer cancel()
	select {
	case <-ch:
	case <-ctx.Done():
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// routes serves the embedded UI alongside two APIs that differ in who blocks on
// them: the browser polls /state and posts /submit, the agent blocks on /wait
// and posts /ask and /reply.
func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	// The API patterns below are more specific, so they win over this catch-all.
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /wait", s.handleWait)
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, startedAs)
	})
	mux.HandleFunc("POST /new", s.handleNew)
	mux.HandleFunc("POST /submit", s.handleSubmit)
	mux.HandleFunc("POST /ask", s.handleAsk)
	mux.HandleFunc("POST /reply", s.handleReply)
	return mux
}

// stateView is the browser's projection of the session. Version is what the
// caller passes back as ?since to wait for the next change.
type stateView struct {
	Version       int      `json:"version"`
	Purpose       string   `json:"purpose"`
	Rounds        []*Round `json:"rounds"`
	RoundComplete bool     `json:"roundComplete"`
}

// handleState returns the session once it has moved past ?since, or unchanged
// once longPoll elapses.
func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	// An absent or unparseable ?since is 0, which yields the current state.
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	v := s.waitFor(r.Context(), since)
	s.mu.Lock()
	defer s.mu.Unlock()

	// A session with no rounds yet must still send [], not null: the page reads
	// this as an array, and a JS default does not cover null.
	rounds := s.state.Rounds
	if rounds == nil {
		rounds = []*Round{}
	}
	writeJSON(w, stateView{
		Version:       v,
		Purpose:       s.state.Purpose,
		Rounds:        rounds,
		RoundComplete: s.state.RoundComplete(),
	})
}

// submission is one user answer awaiting an agent reply.
type submission struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   Kind   `json:"kind"`
	Option int    `json:"option,omitempty"`
	Body   string `json:"body,omitempty"`
}

// waitView is the agent's projection: what to answer, whether the round gate is
// open, and the protocol reminder.
type waitView struct {
	Instructions  string       `json:"instructions"`
	Version       int          `json:"version"`
	RoundComplete bool         `json:"round_complete"`
	Submissions   []submission `json:"submissions"`
}

// handleWait blocks until the agent has work: a submission to answer, or a
// completed round to follow with the next one. Waiting on that predicate rather
// than on a version means the caller never has to track where it left off.
func (s *server) handleWait(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), longPoll)
	defer cancel()

	for {
		s.mu.Lock()
		v, done, ch := s.version, s.state.RoundComplete(), s.changed
		subs := []submission{}
		for _, question := range s.state.Pending() {
			last := question.Entries[len(question.Entries)-1]
			subs = append(subs, submission{
				ID:     question.ID,
				Title:  question.Title,
				Kind:   last.Kind,
				Option: last.Option,
				Body:   last.Body,
			})
		}
		s.mu.Unlock()

		if len(subs) > 0 || done {
			writeJSON(w, waitView{
				Instructions:  waitInstructions,
				Version:       v,
				RoundComplete: done,
				Submissions:   subs,
			})
			return
		}
		select {
		case <-ch:
		case <-ctx.Done():
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

// build identifies the code a process is running. A daemon started at login
// keeps serving whatever it started as, so clients compare this against their
// own to catch an upgrade that left the running service behind.
//
// Version covers package-managed installs, where the path never moves; Exe and
// Mod cover local builds, which report a Version of "(devel)" however often the
// source changes.
type build struct {
	Version string    `json:"version"`
	Exe     string    `json:"exe"`
	Mod     time.Time `json:"mod"`
}

// startedAs is captured at process start. A long-lived server must report the
// binary it is executing, not whatever now sits at that path — an upgrade
// replaces the file underneath it without touching the running process.
var startedAs = currentBuild()

func currentBuild() build {
	b := build{Version: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		b.Version = info.Main.Version
	}
	exe, err := os.Executable()
	if err != nil {
		return b
	}
	b.Exe = exe
	if fi, err := os.Stat(exe); err == nil {
		b.Mod = fi.ModTime()
	}
	return b
}

// Stale reports whether other is running different code than this process.
func (b build) Stale(other build) bool {
	return b.Version != other.Version || b.Exe != other.Exe || !b.Mod.Equal(other.Mod)
}

// handleNew empties the session. A server that has been running since login
// would otherwise hand the next grilling the previous one's rounds.
func (s *server) handleNew(w http.ResponseWriter, r *http.Request) {
	respond(w, s.mutate(func() error {
		s.state = State{}
		return nil
	}), nil)
}

// handleSubmit records a user answer. Unknown, closed, and already-answered
// questions are rejected, so a stale tab cannot submit twice, and an unknown
// kind is rejected here because the page renders an entry by switching on it.
func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string `json:"id"`
		Kind   Kind   `json:"kind"`
		Option int    `json:"option"`
		Body   string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.mutate(func() error {
		question := s.state.find(in.ID)
		switch {
		case question == nil:
			return fmt.Errorf("unknown question %q", in.ID)
		case question.Status != StatusOpen:
			return fmt.Errorf("question %q is already %s", in.ID, question.Status)
		case question.Pending():
			return fmt.Errorf("question %q is awaiting an agent reply", in.ID)
		case !in.Kind.Valid():
			return fmt.Errorf("bad kind %q", in.Kind)
		}
		question.Entries = append(question.Entries, Entry{From: "user", Kind: in.Kind, Option: in.Option, Body: in.Body, At: time.Now()})
		return nil
	})
	respond(w, err, nil)
}

// askPayload is a round as the agent writes it. Purpose is carried only on the
// rounds that set or change it.
type askPayload struct {
	Purpose   string      `json:"purpose,omitempty"`
	Questions []*Question `json:"questions"`
}

// handleAsk pushes a round and returns its assigned ids. It fails while the
// current round holds an open question: that refusal is the round gate.
func (s *server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var in askPayload
	if !decode(w, r, &in) {
		return
	}
	var ids []string
	err := s.mutate(func() error {
		if prev := s.state.Latest(); prev != nil && !s.state.RoundComplete() {
			return fmt.Errorf("round %d is still open", prev.N)
		}
		var err error
		ids, err = s.addRound(in)
		return err
	})
	respond(w, err, map[string]any{"ids": ids})
}

// seed installs a round without the round gate, for --demo.
func (s *server) seed(in askPayload) error {
	return s.mutate(func() error {
		_, err := s.addRound(in)
		return err
	})
}

// addRound appends a round and assigns its question IDs. IDs come from here
// rather than from the agent so that /wait and /reply always agree on them.
// Callers must hold the lock.
func (s *server) addRound(in askPayload) ([]string, error) {
	if len(in.Questions) == 0 {
		return nil, errors.New("a round needs at least one question")
	}
	if in.Purpose != "" {
		s.state.Purpose = in.Purpose
	}
	n := len(s.state.Rounds) + 1
	ids := make([]string, len(in.Questions))
	for i, question := range in.Questions {
		question.ID = fmt.Sprintf("r%dq%d", n, i+1)
		if question.Status == "" {
			question.Status = StatusOpen
		}
		ids[i] = question.ID
	}
	s.state.Rounds = append(s.state.Rounds, &Round{N: n, Questions: in.Questions})
	return ids, nil
}

// handleReply records the agent's response to a submission and sets the
// question's status, which is what tints the card and closes the round.
func (s *server) handleReply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string `json:"id"`
		Status Status `json:"status"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.mutate(func() error {
		question := s.state.find(in.ID)
		switch {
		case question == nil:
			return fmt.Errorf("unknown question %q", in.ID)
		case !in.Status.Valid():
			return fmt.Errorf("bad status %q", in.Status)
		}
		question.Status = in.Status
		question.Entries = append(question.Entries, Entry{From: "agent", Body: in.Note, At: time.Now()})
		return nil
	})
	respond(w, err, nil)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	// Question bodies are HTML; escaping their angle brackets would leave the
	// agent reading < in its own /wait payloads.
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// decode reads the JSON request body into v. It reports false after writing a
// 400, in which case the handler must return without touching the response.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// respond turns a rejected mutation into a 400, so that a bad `grilled-cheese
// reply --id` exits non-zero instead of vanishing.
func respond(w http.ResponseWriter, err error, body map[string]any) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	writeJSON(w, body)
}
