package collector

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/intelligexhq/argus/internal/classify"
)

// These tests exercise the real OS — the collectors are thin wrappers around
// gopsutil and have no seams. We assert only what must hold on any healthy
// host: the current test process appears in the process table, and any
// connection returned satisfies the documented filter contract.

func TestProcessCollector_Name(t *testing.T) {
	if got := NewProcessCollector().Name(); got != "process" {
		t.Errorf("Name() = %q, want %q", got, "process")
	}
}

func TestProcessCollector_CollectFindsCurrentProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := NewProcessCollector().Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Processes) == 0 {
		t.Fatal("Collect returned no processes; expected at least the test runner")
	}

	want := int32(os.Getpid())
	for _, p := range res.Processes {
		if p.PID == want {
			return
		}
	}
	t.Errorf("current process pid %d not found in collected processes", want)
}

func TestNetworkCollector_Name(t *testing.T) {
	if got := NewNetworkCollector(classify.NewDefault()).Name(); got != "network" {
		t.Errorf("Name() = %q, want %q", got, "network")
	}
}

func TestNetworkCollector_CollectFilterContract(t *testing.T) {
	// We can't guarantee the test process has any inet connections, so the
	// only invariant is the documented filter: every returned connection must
	// have a non-zero PID, a non-empty remote address, and a non-zero port.
	// Skip on platforms where socket enumeration is denied (rare in CI but
	// possible inside locked-down sandboxes).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := NewNetworkCollector(classify.NewDefault()).Collect(ctx)
	if err != nil {
		t.Skipf("network collector unavailable on this host (%v) — skipping", err)
	}
	for _, c := range res.Connections {
		if c.PID == 0 || c.RemoteIP == "" || c.RemotePort == 0 {
			t.Errorf("filter contract violated: %+v", c)
		}
		if c.Classification == "" {
			t.Errorf("classification missing on %+v", c)
		}
	}
}
