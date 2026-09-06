package main

import "time"

// Status is a question's position in the round. Every question starts open and
// the round gate stays shut until none are.
type Status string

const (
	StatusOpen     Status = "open"
	StatusSettled  Status = "settled"
	StatusRejected Status = "rejected"
)

// Kind is how the user answered: picked an option, rejected the proposal
// outright, or wrote prose.
type Kind string

const (
	KindOption Kind = "option"
	KindReject Kind = "reject"
	KindText   Kind = "text"
)

// Entry is one turn in a question's thread. Kind and Option carry meaning only
// when From is "user".
type Entry struct {
	From   string    `json:"from"` // "user" or "agent"
	Kind   Kind      `json:"kind,omitempty"`
	Option int       `json:"option,omitempty"`
	Body   string    `json:"body,omitempty"`
	At     time.Time `json:"at"`
}

// Question is one node of the design tree. Body, Options and agent Entry bodies
// hold agent-authored HTML and reach the page unescaped, so nothing from an
// untrusted source may be routed into them.
type Question struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Options     []string `json:"options,omitempty"`
	Recommended int      `json:"recommended,omitempty"` // 1-based index into Options
	Status      Status   `json:"status"`
	Entries     []Entry  `json:"entries,omitempty"`
}

// Pending reports whether the agent owes this question a reply. The thread
// alternates, so an unanswered submission is one the user spoke last on.
func (q *Question) Pending() bool {
	n := len(q.Entries)
	return n > 0 && q.Entries[n-1].From == "user"
}

// Round is one batch of questions answered together. Rounds are numbered from
// 1 and never reordered.
type Round struct {
	N         int         `json:"n"`
	Questions []*Question `json:"questions"`
}

// Complete reports whether the round gate is open. An empty round never
// completes, so a round pushed with no questions cannot unblock the next.
func (r *Round) Complete() bool {
	if len(r.Questions) == 0 {
		return false
	}
	for _, q := range r.Questions {
		if q.Status == StatusOpen {
			return false
		}
	}
	return true
}

// State is the whole session. The serving process is its only writer; the
// browser and the agent both mutate it through the HTTP API.
type State struct {
	Purpose string   `json:"purpose"`
	Rounds  []*Round `json:"rounds"`
}

// find returns a pointer into the live state, so callers mutate the question in
// place. Nil when no round holds the id.
func (s *State) find(id string) *Question {
	for _, r := range s.Rounds {
		for _, q := range r.Questions {
			if q.ID == id {
				return q
			}
		}
	}
	return nil
}

// Pending lists every question awaiting an agent reply, oldest round first.
func (s *State) Pending() []*Question {
	var out []*Question
	for _, r := range s.Rounds {
		for _, q := range r.Questions {
			if q.Pending() {
				out = append(out, q)
			}
		}
	}
	return out
}

// Latest returns the round the user is currently working, or nil before the
// first ask.
func (s *State) Latest() *Round {
	if len(s.Rounds) == 0 {
		return nil
	}
	return s.Rounds[len(s.Rounds)-1]
}

// RoundComplete gates the next ask: true once every question in the latest
// round is settled and the agent has replied to every submission.
func (s *State) RoundComplete() bool {
	r := s.Latest()
	return r != nil && r.Complete() && len(s.Pending()) == 0
}
