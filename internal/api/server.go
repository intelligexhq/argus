// Package api exposes the collected data over a local HTTP/JSON read API.
// It is the only outward-facing surface; UIs (menu bar, CLI) are clients of it
// and never touch the SQLite file directly.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/intelligexhq/argus/internal/model"
	"github.com/intelligexhq/argus/internal/store"
)

//go:embed openapi.yaml
var openapiSpec []byte

//go:embed openapi.html
var openapiHTML []byte

type Server struct {
	st  *store.Store
	srv *http.Server
}

func NewServer(st *store.Store) *Server {
	s := &Server{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/agents", s.handleAgents)
	mux.HandleFunc("GET /v1/processes", s.handleProcesses)
	mux.HandleFunc("GET /v1/connections", s.handleConnections)
	mux.HandleFunc("GET /v1/openapi", s.handleOpenAPIUI)
	mux.HandleFunc("GET /v1/openapi.yaml", s.handleOpenAPI)

	// Order matters: requestID is outermost so access-log and recover both see
	// the ID; recover sits inside access-log so a panic-turned-500 is reflected
	// in the logged status.
	handler := chain(mux,
		requestIDMiddleware,
		accessLogMiddleware,
		recoverMiddleware,
	)

	s.srv = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	return s
}

func (s *Server) Handler() http.Handler              { return s.srv.Handler }
func (s *Server) Serve(ln net.Listener) error        { return s.srv.Serve(ln) }
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, map[string]string{"status": "ok"})
}

// expandedAgent extends model.Agent with optional joined detail for the
// ?expand= query parameter. Fields omitted when not requested keep the default
// /v1/agents response compact.
type expandedAgent struct {
	model.Agent
	Processes   []model.Process    `json:"processes,omitempty"`
	Connections []model.Connection `json:"connections,omitempty"`
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	agents, err := s.st.ListAgents(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	wantProcs, wantConns := parseExpand(r.URL.Query().Get("expand"))
	if !wantProcs && !wantConns {
		s.writeJSON(w, map[string]any{"agents": agents})
		return
	}

	var procsByAgent map[string][]model.Process
	if wantProcs {
		procs, err := s.st.ListProcesses(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		procsByAgent = map[string][]model.Process{}
		for _, p := range procs {
			if p.AgentID != "" {
				procsByAgent[p.AgentID] = append(procsByAgent[p.AgentID], p)
			}
		}
	}

	var connsByAgent map[string][]model.Connection
	if wantConns {
		conns, err := s.st.ListConnections(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		connsByAgent = map[string][]model.Connection{}
		for _, c := range conns {
			if c.AgentID != "" {
				connsByAgent[c.AgentID] = append(connsByAgent[c.AgentID], c)
			}
		}
	}

	out := make([]expandedAgent, len(agents))
	for i, a := range agents {
		out[i] = expandedAgent{Agent: a}
		if wantProcs {
			out[i].Processes = procsByAgent[a.ID]
		}
		if wantConns {
			out[i].Connections = connsByAgent[a.ID]
		}
	}
	s.writeJSON(w, map[string]any{"agents": out})
}

// parseExpand reads a comma-separated expand list. Unknown values are ignored
// so older clients passing future tokens stay forward-compatible.
func parseExpand(s string) (procs, conns bool) {
	for _, part := range strings.Split(s, ",") {
		switch strings.TrimSpace(part) {
		case "processes":
			procs = true
		case "connections":
			conns = true
		}
	}
	return
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	procs, err := s.st.ListProcesses(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"processes": procs})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := s.st.ListConnections(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]any{"connections": conns})
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiSpec)
}

func (s *Server) handleOpenAPIUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(openapiHTML)
}
