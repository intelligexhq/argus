package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/intelligexhq/argus/internal/model"
	"github.com/intelligexhq/argus/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestHTTP(t *testing.T, st *store.Store) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewServer(st).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newTestHTTP(t, newTestStore(t))
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Errorf("want status=ok, got %v", got)
	}
}

func TestAgentsHandlerWrapsStoreOutput(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := st.WriteSnapshot(now,
		[]model.Agent{{ID: "abc", Type: "claude-code", Name: "claude", Confidence: 0.85, FirstSeen: now, LastSeen: now}},
		[]model.Process{{PID: 1, PPID: 0, Name: "claude", Exe: "/bin/claude", StartedAt: now, AgentID: "abc"}},
		nil,
	)
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	srv := newTestHTTP(t, st)
	resp, err := http.Get(srv.URL + "/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var body struct {
		Agents []model.Agent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || body.Agents[0].Type != "claude-code" || body.Agents[0].ID != "abc" {
		t.Errorf("unexpected agents payload: %+v", body)
	}
}

func TestAgentsHandler_ExpandProcessesAndConnections(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := st.WriteSnapshot(now,
		[]model.Agent{{ID: "abc", Type: "claude-code", Name: "claude", Confidence: 0.85, FirstSeen: now, LastSeen: now}},
		[]model.Process{
			{PID: 1, PPID: 0, Name: "claude", Exe: "/bin/claude", Cmdline: "claude --serve", StartedAt: now, AgentID: "abc"},
			{PID: 2, PPID: 1, Name: "rg", Exe: "/bin/rg", StartedAt: now, AgentID: "abc"},
			{PID: 99, PPID: 0, Name: "unrelated", StartedAt: now}, // not tied to any agent
		},
		[]model.Connection{
			{PID: 1, RemoteIP: "203.0.113.1", RemoteHost: "node-203-0-113-1.example.net", RemotePort: 443, Endpoint: "Anthropic API", Classification: "public", ObservedAt: now, AgentID: "abc"},
		},
	)
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	srv := newTestHTTP(t, st)

	type respAgent struct {
		ID          string             `json:"id"`
		Processes   []model.Process    `json:"processes"`
		Connections []model.Connection `json:"connections"`
	}
	type resp struct {
		Agents []respAgent `json:"agents"`
	}

	t.Run("no expand omits detail", func(t *testing.T) {
		r, err := http.Get(srv.URL + "/v1/agents")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var got resp
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Agents) != 1 {
			t.Fatalf("want 1 agent, got %d", len(got.Agents))
		}
		if got.Agents[0].Processes != nil || got.Agents[0].Connections != nil {
			t.Errorf("default response leaked detail: %+v", got.Agents[0])
		}
	})

	t.Run("expand=processes embeds only agent-tagged procs", func(t *testing.T) {
		r, err := http.Get(srv.URL + "/v1/agents?expand=processes")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var got resp
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Agents[0].Processes) != 2 {
			t.Errorf("want 2 processes for agent abc, got %d", len(got.Agents[0].Processes))
		}
		if got.Agents[0].Connections != nil {
			t.Errorf("connections should not appear when not requested")
		}
	})

	t.Run("expand=processes,connections embeds both", func(t *testing.T) {
		r, err := http.Get(srv.URL + "/v1/agents?expand=processes,connections")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var got resp
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Agents[0].Processes) != 2 {
			t.Errorf("processes count = %d, want 2", len(got.Agents[0].Processes))
		}
		if len(got.Agents[0].Connections) != 1 || got.Agents[0].Connections[0].Endpoint != "Anthropic API" {
			t.Errorf("connections payload wrong: %+v", got.Agents[0].Connections)
		}
	})

	t.Run("unknown expand value is ignored", func(t *testing.T) {
		r, err := http.Get(srv.URL + "/v1/agents?expand=processes,bogus")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Errorf("status %d, want 200 (unknown tokens should be silently ignored)", r.StatusCode)
		}
		var got resp
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Agents[0].Processes) != 2 {
			t.Errorf("processes should still expand: %+v", got.Agents[0])
		}
	})
}

func TestOpenAPIYaml(t *testing.T) {
	srv := newTestHTTP(t, newTestStore(t))
	resp, err := http.Get(srv.URL + "/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("content-type = %q, want application/yaml", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(b), "openapi:") {
		t.Errorf("body does not look like OpenAPI yaml: %.40q", b)
	}
}

func TestOpenAPIUI(t *testing.T) {
	srv := newTestHTTP(t, newTestStore(t))
	resp, err := http.Get(srv.URL + "/v1/openapi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "swagger-ui") {
		t.Errorf("html missing swagger-ui reference: %.80q", b)
	}
}
