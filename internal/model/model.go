// Package model holds the canonical entities the collector produces and serves.
package model

import "time"

// Process is a single OS process observed during a scan.
type Process struct {
	PID       int32     `json:"pid"`
	PPID      int32     `json:"ppid"`
	Name      string    `json:"name"`
	Exe       string    `json:"exe"`
	Cmdline   string    `json:"cmdline"`
	StartedAt time.Time `json:"started_at"`
	AgentID   string    `json:"agent_id,omitempty"`
}

// Connection is a remote endpoint signal tied to a process. Source disambiguates
// how the signal was observed: a live OS socket ("socket") or a declared
// endpoint read from the process environment ("env").
type Connection struct {
	PID            int32     `json:"pid"`
	RemoteAddr     string    `json:"remote_addr"`               // empty for env-declared endpoints with no resolved IP
	RemotePort     uint32    `json:"remote_port"`
	Endpoint       string    `json:"endpoint,omitempty"`        // matched model-service label, if any
	Classification string    `json:"classification"`            // public | private | loopback | unknown
	ObservedAt     time.Time `json:"observed_at"`
	AgentID        string    `json:"agent_id,omitempty"`
	Source         string    `json:"source"`                    // socket | env
	SourceDetail   string    `json:"source_detail,omitempty"`   // for env: matched env var name
}

// Agent is a discovered AI agent: a correlated identity over one or more processes.
type Agent struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`       // e.g. claude-code, ollama, llm-client
	Name       string    `json:"name"`
	Confidence float64   `json:"confidence"` // 0..1
	PIDs       []int32   `json:"pids"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}
