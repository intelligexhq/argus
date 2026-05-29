// Package correlate turns raw process/connection observations into Agent
// identities. This is the heart of discovery: deciding "is this an agent, which
// one, and how sure are we", then attaching the processes it spawned.
package correlate

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	"github.com/intelligexhq/argus/internal/collector"
	"github.com/intelligexhq/argus/internal/model"
)

// fingerprint matches an agent type by keyword in exe/cmdline/name.
type fingerprint struct {
	agentType string
	keywords  []string
}

var fingerprints = []fingerprint{
	{"claude-code", []string{"claude"}},
	{"cursor", []string{"cursor"}},
	{"github-copilot", []string{"copilot"}},
	{"aider", []string{"aider"}},
	{"ollama", []string{"ollama"}},
	{"llama.cpp", []string{"llama-server", "llama.cpp"}},
	{"openai-sdk", []string{"openai"}},
	{"langchain", []string{"langchain", "langgraph"}},
}

const (
	confKeyword = 0.6  // matched a known agent binary/cmdline
	confEgress  = 0.85 // talks to a known model endpoint
)

// interpreters run their real workload from argv, so their cmdline is trusted
// for fingerprint matching (unlike shells or arbitrary binaries).
var interpreters = map[string]bool{
	"node": true, "python": true, "python3": true,
	"deno": true, "bun": true, "ruby": true, "java": true,
}

// Correlate mutates res (setting AgentID on processes/connections) and returns
// the discovered agents.
func Correlate(now time.Time, res *collector.Result) []model.Agent {
	connByPID := map[int32][]int{}
	for i := range res.Connections {
		c := &res.Connections[i]
		connByPID[c.PID] = append(connByPID[c.PID], i)
	}

	agents := map[string]*model.Agent{}
	pidKey := map[int32]string{}

	keyOf := func(typ, exe string) string {
		sum := sha1.Sum([]byte(typ + "|" + exe))
		return hex.EncodeToString(sum[:])[:12]
	}
	ensure := func(typ, exe, name string, conf float64) string {
		k := keyOf(typ, exe)
		a, ok := agents[k]
		if !ok {
			a = &model.Agent{ID: k, Type: typ, Name: name, Confidence: conf, FirstSeen: now, LastSeen: now}
			agents[k] = a
		}
		if conf > a.Confidence {
			a.Confidence = conf
		}
		return k
	}

	// Pass 1: classify each process by binary fingerprint and/or model egress.
	for i := range res.Processes {
		p := &res.Processes[i]

		// Identity lives in the binary (name + exe basename). The cmdline is
		// only trusted for interpreters, whose real workload is in argv (e.g.
		// `node .../claude`, `python -m crewai`). This stops a plain shell whose
		// command line merely *mentions* "claude" from being labelled an agent.
		exeBase := strings.ToLower(filepath.Base(p.Exe))
		nameHay := strings.ToLower(p.Name) + " " + exeBase
		isInterp := interpreters[exeBase] || interpreters[strings.ToLower(p.Name)]
		cmd := strings.ToLower(p.Cmdline)

		typ := ""
		for _, fp := range fingerprints {
			for _, kw := range fp.keywords {
				if kw == "" {
					continue
				}
				if strings.Contains(nameHay, kw) || (isInterp && strings.Contains(cmd, kw)) {
					typ = fp.agentType
					break
				}
			}
			if typ != "" {
				break
			}
		}
		conf := 0.0
		if typ != "" {
			conf = confKeyword
		}

		for _, ci := range connByPID[p.PID] {
			if res.Connections[ci].Endpoint != "" {
				if typ == "" {
					typ = "llm-client" // unknown binary, but it's talking to a model
				}
				if conf < confEgress {
					conf = confEgress
				}
				break
			}
		}

		if typ == "" {
			continue
		}
		pidKey[p.PID] = ensure(typ, p.Exe, displayName(p), conf)
	}

	// Pass 2: attach direct children of agent processes (the "subagents it spun"
	// signal). One level here; deeper trees are an event-collector concern.
	for i := range res.Processes {
		p := &res.Processes[i]
		if _, ok := pidKey[p.PID]; ok {
			continue
		}
		if k, ok := pidKey[p.PPID]; ok {
			pidKey[p.PID] = k
		}
	}

	// Stamp ids back onto observations and roll up pids / last_seen.
	for i := range res.Processes {
		p := &res.Processes[i]
		if k, ok := pidKey[p.PID]; ok {
			p.AgentID = k
			a := agents[k]
			a.PIDs = append(a.PIDs, p.PID)
			a.LastSeen = now
		}
	}
	for i := range res.Connections {
		c := &res.Connections[i]
		if k, ok := pidKey[c.PID]; ok {
			c.AgentID = k
		}
	}

	out := make([]model.Agent, 0, len(agents))
	for _, a := range agents {
		out = append(out, *a)
	}
	return out
}

func displayName(p *model.Process) string {
	if p.Name != "" {
		return p.Name
	}
	if p.Exe != "" {
		return filepath.Base(p.Exe)
	}
	return "unknown"
}
