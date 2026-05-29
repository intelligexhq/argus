//go:build !darwin

package collector

import (
	"context"

	"github.com/shirou/gopsutil/v3/process"
)

// readProcessEnv on Linux reads /proc/<pid>/environ and on Windows reads the
// process environment block via toolhelp — both via gopsutil. Both platforms
// implement Environ correctly.
func readProcessEnv(ctx context.Context, pid int32) ([]string, error) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil, err
	}
	return p.EnvironWithContext(ctx)
}
