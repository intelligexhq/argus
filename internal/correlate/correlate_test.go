package correlate

import (
	"testing"
	"time"

	"github.com/intelligexhq/argus/internal/collector"
	"github.com/intelligexhq/argus/internal/model"
)

func TestCorrelate_BinaryFingerprint(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 100, PPID: 1, Name: "claude", Exe: "/usr/local/bin/claude"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 1 || agents[0].Type != "claude-code" {
		t.Fatalf("want one claude-code agent, got %+v", agents)
	}
	if res.Processes[0].AgentID == "" {
		t.Errorf("expected AgentID stamped on process")
	}
	if agents[0].Confidence != confKeyword {
		t.Errorf("confidence = %v, want %v", agents[0].Confidence, confKeyword)
	}
}

func TestCorrelate_InterpreterCmdline(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 200, PPID: 1, Name: "node", Exe: "/usr/local/bin/node", Cmdline: "node /opt/claude/dist/index.js"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 1 || agents[0].Type != "claude-code" {
		t.Fatalf("interpreter+cmdline should match claude-code; got %+v", agents)
	}
}

func TestCorrelate_PlainShellIgnoresCmdline(t *testing.T) {
	// Invariant from correlate.go: a shell whose cmdline merely mentions an
	// agent keyword must not classify, because shells aren't interpreters.
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 300, PPID: 1, Name: "bash", Exe: "/bin/bash", Cmdline: "bash -c 'echo claude'"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 0 {
		t.Fatalf("shell mentioning 'claude' must not classify, got %+v", agents)
	}
}

func TestCorrelate_EgressOnlyClassifiesAsLLMClient(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 400, PPID: 1, Name: "mystery", Exe: "/opt/x/mystery"},
		},
		Connections: []model.Connection{
			{PID: 400, Endpoint: "Anthropic API"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 1 || agents[0].Type != "llm-client" {
		t.Fatalf("egress-only should classify as llm-client; got %+v", agents)
	}
	if agents[0].Confidence != confEgress {
		t.Errorf("confidence = %v, want %v", agents[0].Confidence, confEgress)
	}
}

func TestCorrelate_KeywordPlusEgressTakesMaxConfidence(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 500, PPID: 1, Name: "claude", Exe: "/usr/bin/claude"},
		},
		Connections: []model.Connection{
			{PID: 500, Endpoint: "Anthropic API"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 1 {
		t.Fatalf("want one agent, got %d", len(agents))
	}
	if agents[0].Confidence != confEgress {
		t.Errorf("confidence = %v, want %v (max of keyword/egress)", agents[0].Confidence, confEgress)
	}
}

func TestCorrelate_ChildAttachesToParentAgent(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 600, PPID: 1, Name: "claude", Exe: "/usr/bin/claude"},
			{PID: 601, PPID: 600, Name: "rg", Exe: "/usr/bin/rg"}, // ripgrep child
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 1 {
		t.Fatalf("want one parent agent, got %d (%+v)", len(agents), agents)
	}
	parent := res.Processes[0].AgentID
	child := res.Processes[1].AgentID
	if parent == "" || child != parent {
		t.Errorf("child should inherit parent AgentID; parent=%q child=%q", parent, child)
	}
	if len(agents[0].PIDs) != 2 {
		t.Errorf("want 2 PIDs rolled up onto agent, got %v", agents[0].PIDs)
	}
}

func TestCorrelate_OrphanProcessIgnored(t *testing.T) {
	res := &collector.Result{
		Processes: []model.Process{
			{PID: 700, PPID: 1, Name: "rg", Exe: "/usr/bin/rg"},
		},
	}
	agents := Correlate(time.Now(), res)
	if len(agents) != 0 {
		t.Fatalf("non-agent process with no agent parent must not classify, got %+v", agents)
	}
}

func TestCorrelate_StableAgentIDAcrossCalls(t *testing.T) {
	proc := model.Process{PID: 800, PPID: 1, Name: "ollama", Exe: "/usr/bin/ollama"}
	r1 := &collector.Result{Processes: []model.Process{proc}}
	r2 := &collector.Result{Processes: []model.Process{proc}}
	a1 := Correlate(time.Now(), r1)
	a2 := Correlate(time.Now(), r2)
	if a1[0].ID != a2[0].ID {
		t.Errorf("agent ID not stable across calls: %q vs %q", a1[0].ID, a2[0].ID)
	}
}
