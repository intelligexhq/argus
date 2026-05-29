package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/intelligexhq/argus/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestOpen_MigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	st1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = st1.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = st2.Close()
}

func TestWriteSnapshot_AgentRollsUpPIDs(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	agents := []model.Agent{
		{ID: "abc", Type: "claude-code", Name: "claude", Confidence: 0.85, FirstSeen: now, LastSeen: now},
	}
	procs := []model.Process{
		{PID: 10, PPID: 1, Name: "claude", Exe: "/bin/claude", StartedAt: now, AgentID: "abc"},
		{PID: 11, PPID: 10, Name: "rg", Exe: "/bin/rg", StartedAt: now, AgentID: "abc"},
		{PID: 12, PPID: 1, Name: "unrelated", Exe: "/bin/unrelated", StartedAt: now}, // no AgentID
	}
	if err := st.WriteSnapshot(now, agents, procs, nil); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	got, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 agent, got %d", len(got))
	}
	a := got[0]
	if a.ID != "abc" || a.Type != "claude-code" {
		t.Errorf("agent fields wrong: %+v", a)
	}
	if len(a.PIDs) != 2 {
		t.Errorf("want 2 PIDs rolled up (only the agent-tagged ones), got %v", a.PIDs)
	}
}

func TestWriteSnapshot_ReplacesProcsAndConns(t *testing.T) {
	// Snapshot semantics: each write replaces the live process/connection table.
	st := newTestStore(t)
	t0 := time.Now().UTC().Truncate(time.Millisecond)

	err := st.WriteSnapshot(t0, nil,
		[]model.Process{{PID: 1, PPID: 0, Name: "a", StartedAt: t0}, {PID: 2, PPID: 1, Name: "b", StartedAt: t0}},
		[]model.Connection{{PID: 1, RemoteIP: "1.2.3.4", RemotePort: 443, Classification: "public", ObservedAt: t0}},
	)
	if err != nil {
		t.Fatalf("WriteSnapshot 1: %v", err)
	}

	t1 := t0.Add(time.Second)
	err = st.WriteSnapshot(t1, nil,
		[]model.Process{{PID: 3, PPID: 0, Name: "c", StartedAt: t1}},
		nil,
	)
	if err != nil {
		t.Fatalf("WriteSnapshot 2: %v", err)
	}

	procs, err := st.ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(procs) != 1 || procs[0].PID != 3 {
		t.Errorf("snapshot did not replace processes; got %+v", procs)
	}

	conns, err := st.ListConnections()
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 0 {
		t.Errorf("snapshot did not replace connections; got %+v", conns)
	}
}

func TestWriteSnapshot_AgentUpsertPreservesFirstSeen(t *testing.T) {
	// On re-write of an existing agent ID, first_seen must NOT be overwritten;
	// last_seen, confidence, and name should refresh.
	st := newTestStore(t)
	t0 := time.Now().UTC().Truncate(time.Millisecond)
	t1 := t0.Add(5 * time.Minute)

	err := st.WriteSnapshot(t0,
		[]model.Agent{{ID: "foo", Type: "ollama", Name: "ollama", Confidence: 0.6, FirstSeen: t0, LastSeen: t0}},
		nil, nil)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}

	err = st.WriteSnapshot(t1,
		[]model.Agent{{ID: "foo", Type: "ollama", Name: "ollama-renamed", Confidence: 0.9, FirstSeen: t1, LastSeen: t1}},
		nil, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 agent, got %d", len(got))
	}
	a := got[0]
	if !a.FirstSeen.Equal(t0) {
		t.Errorf("first_seen overwritten: got %v, want %v", a.FirstSeen, t0)
	}
	if !a.LastSeen.Equal(t1) {
		t.Errorf("last_seen not refreshed: got %v, want %v", a.LastSeen, t1)
	}
	if a.Confidence != 0.9 {
		t.Errorf("confidence not updated: got %v, want 0.9", a.Confidence)
	}
	if a.Name != "ollama-renamed" {
		t.Errorf("name not updated: got %q", a.Name)
	}
}

func TestListAgents_NoProcessesReturnsEmptyPIDsSlice(t *testing.T) {
	// The API contract is that PIDs is always a non-nil slice (so JSON encodes
	// as [] not null). Verify the empty case.
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := st.WriteSnapshot(now,
		[]model.Agent{{ID: "x", Type: "t", Name: "n", FirstSeen: now, LastSeen: now}},
		nil, nil)
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	got, err := st.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 1 || got[0].PIDs == nil {
		t.Errorf("want non-nil empty PIDs slice, got %+v", got)
	}
}

func TestListConnections_Roundtrip(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := model.Connection{
		PID: 99, RemoteIP: "203.0.113.1", RemoteHost: "node-203-0-113-1.example.net", RemotePort: 443,
		Endpoint: "Anthropic API", Classification: "public",
		ObservedAt: now, AgentID: "abc",
	}
	if err := st.WriteSnapshot(now, nil, nil, []model.Connection{c}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	got, err := st.ListConnections()
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 connection, got %d", len(got))
	}
	g := got[0]
	if g.PID != c.PID || g.RemoteIP != c.RemoteIP || g.RemoteHost != c.RemoteHost || g.RemotePort != c.RemotePort ||
		g.Endpoint != c.Endpoint || g.Classification != c.Classification || g.AgentID != c.AgentID {
		t.Errorf("roundtrip mismatch: got %+v want %+v", g, c)
	}
	if !g.ObservedAt.Equal(now) {
		t.Errorf("ObservedAt mismatch: got %v want %v", g.ObservedAt, now)
	}
	// Source defaults to "socket" when not set on the input row.
	if g.Source != "socket" {
		t.Errorf("Source default = %q, want socket", g.Source)
	}
}

func TestListConnections_EnvSourceRoundtrip(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := model.Connection{
		PID: 42, RemoteHost: "gpu.internal", RemotePort: 11434,
		Endpoint: "Ollama", Classification: "private",
		ObservedAt: now, AgentID: "xyz",
		Source: "env", SourceDetail: "OLLAMA_HOST",
	}
	if err := st.WriteSnapshot(now, nil, nil, []model.Connection{c}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	got, err := st.ListConnections()
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 connection, got %d", len(got))
	}
	g := got[0]
	if g.Source != "env" || g.SourceDetail != "OLLAMA_HOST" {
		t.Errorf("env-source not preserved: got source=%q detail=%q", g.Source, g.SourceDetail)
	}
}
