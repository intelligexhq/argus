package collector

import (
	"context"
	"strings"
	"time"

	"github.com/intelligexhq/argus/internal/model"
	"github.com/shirou/gopsutil/v3/process"
)

// ProcessCollector enumerates the OS process table (pid, parent, exe, cmdline).
// It is the base signal: cheap, unprivileged for the current user, cross-platform.
type ProcessCollector struct{}

func NewProcessCollector() *ProcessCollector { return &ProcessCollector{} }

func (c *ProcessCollector) Name() string { return "process" }

func (c *ProcessCollector) Collect(ctx context.Context) (Result, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return Result{}, err
	}
	out := make([]model.Process, 0, len(procs))
	for _, p := range procs {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		// Per-process metadata is best-effort: short-lived or protected
		// processes may deny individual fields, which is fine — we keep what
		// we can rather than dropping the row.
		name, _ := p.NameWithContext(ctx)
		exe, _ := p.ExeWithContext(ctx)
		cmdline, _ := p.CmdlineWithContext(ctx)
		ppid, _ := p.PpidWithContext(ctx)

		var started time.Time
		if ms, err := p.CreateTimeWithContext(ctx); err == nil {
			started = time.UnixMilli(ms)
		}

		out = append(out, model.Process{
			PID:       p.Pid,
			PPID:      ppid,
			Name:      name,
			Exe:       exe,
			Cmdline:   strings.TrimSpace(cmdline),
			StartedAt: started,
		})
	}
	return Result{Processes: out}, nil
}
