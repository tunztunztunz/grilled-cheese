package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSession walks one round through the protocol: ask, submit, wait, reply.
// It is the check on the round gate, on ID assignment, and on pending being
// derived from the thread rather than tracked separately.
func TestSession(t *testing.T) {
	s, err := newServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mux := s.routes()

	do := func(method, path, body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}

	code, out := do("POST", "/ask", `{"purpose":"p","questions":[{"title":"A"},{"title":"B","options":["x","y"]}]}`)
	if code != 200 || !strings.Contains(out, `"r1q2"`) {
		t.Fatalf("ask: %d %s", code, out)
	}

	if code, _ := do("POST", "/ask", `{"questions":[{"title":"C"}]}`); code != 400 {
		t.Errorf("second ask while round 1 is open: got %d, want 400", code)
	}
	if code, _ := do("POST", "/submit", `{"id":"nope","kind":"reject"}`); code != 400 {
		t.Errorf("submit to unknown id: got %d, want 400", code)
	}

	if code, _ := do("POST", "/submit", `{"id":"r1q2","kind":"option","option":1}`); code != 200 {
		t.Fatal("submit r1q2")
	}
	if code, _ := do("POST", "/submit", `{"id":"r1q2","kind":"text","body":"again"}`); code != 400 {
		t.Errorf("second submit before the agent replies: got %d, want 400", code)
	}

	var got waitView
	_, out = do("GET", "/wait", "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Submissions) != 1 || got.Submissions[0].ID != "r1q2" || got.RoundComplete {
		t.Fatalf("wait: %+v", got)
	}

	do("POST", "/reply", `{"id":"r1q2","status":"settled","note":"<p>ok</p>"}`)
	if s.state.RoundComplete() {
		t.Error("round completed with r1q1 still open")
	}

	do("POST", "/submit", `{"id":"r1q1","kind":"reject"}`)
	do("POST", "/reply", `{"id":"r1q1","status":"rejected","note":"<p>noted</p>"}`)
	if !s.state.RoundComplete() {
		t.Fatal("round should be complete once every question is settled")
	}

	if code, _ := do("POST", "/ask", `{"questions":[{"title":"C"}]}`); code != 200 {
		t.Error("next round should be allowed once the gate opens")
	}
	if q := s.state.find("r2q1"); q == nil || q.Status != StatusOpen {
		t.Errorf("round 2 question: %+v", q)
	}

	// State survives a restart, so a reload or a crashed browser loses nothing.
	reopened, err := newServer(s.path[:strings.LastIndex(s.path, "/")])
	if err != nil || reopened.state.Purpose != "p" || len(reopened.state.Rounds) != 2 {
		t.Fatalf("reload: %v %+v", err, reopened.state)
	}
}

// TestStaleAddrFile covers the case cleanup cannot: a killed server leaves its
// addr behind, so an unreachable address has to read as a dead session rather
// than as a raw dial error.
func TestStaleAddrFile(t *testing.T) {
	dir := t.TempDir()
	// Port 1 is reserved and never listening, so this refuses immediately.
	if err := os.WriteFile(filepath.Join(dir, "addr"), []byte("127.0.0.1:1"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := call(dir, "GET", "/wait", nil)
	if err == nil || !strings.Contains(err.Error(), "grilled-cheese serve") {
		t.Fatalf("stale addr should name the dead session, got: %v", err)
	}
}

// TestStaleBuild covers the trap a long-lived server sets: it keeps serving the
// code it started as, so a rebuild or a versioned plugin path must be caught.
func TestStaleBuild(t *testing.T) {
	now := time.Now()
	mine := build{Exe: "/plugin/1.1.0/grill", Mod: now}

	for _, tc := range []struct {
		name    string
		running build
		stale   bool
	}{
		{"same binary", build{Exe: "/plugin/1.1.0/grill", Mod: now}, false},
		{"rebuilt in place", build{Exe: "/plugin/1.1.0/grill", Mod: now.Add(-time.Hour)}, true},
		{"previous plugin version", build{Exe: "/plugin/1.0.0/grill", Mod: now}, true},
	} {
		if got := mine.Stale(tc.running); got != tc.stale {
			t.Errorf("%s: Stale() = %v, want %v", tc.name, got, tc.stale)
		}
	}
}

// TestRejectedStatusIsInvalidInput pins the status enum: anything outside the
// three known values is a 400, not a silently stored string.
func TestRejectedStatusIsInvalidInput(t *testing.T) {
	s, _ := newServer(t.TempDir())
	mux := s.routes()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/ask", strings.NewReader(`{"questions":[{"title":"A"}]}`)))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/reply", strings.NewReader(`{"id":"r1q1","status":"maybe"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad status should be rejected: got %d", w.Code)
	}
}
