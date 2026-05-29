//go:build darwin

package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// readProcessEnv reads a Darwin process's environment via sysctl
// KERN_PROCARGS2. gopsutil's Environ() returns "not implemented yet" on
// macOS, so we hit the kernel directly. The kernel enforces same-UID access,
// which preserves the unprivileged-by-default posture.
func readProcessEnv(_ context.Context, pid int32) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil {
		return nil, err
	}
	return parseProcargs2(buf)
}

// parseProcargs2 decodes the KERN_PROCARGS2 buffer layout:
//
//	[argc:           int32 little-endian]
//	[exec path:      null-terminated string]
//	[zero padding:   zero or more 0x00 bytes for alignment]
//	[argv[0..argc-1]: each null-terminated]
//	[env[0..N-1]:    each null-terminated, terminated by an empty entry or EOF]
//
// Returns env entries as "KEY=VALUE" strings.
func parseProcargs2(buf []byte) ([]string, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("procargs2 buffer too small: %d bytes", len(buf))
	}
	argc := binary.LittleEndian.Uint32(buf[:4])
	rest := buf[4:]

	// Skip exec path.
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return nil, fmt.Errorf("procargs2: no exec path terminator")
	}
	rest = rest[n+1:]

	// Skip alignment padding (run of zero bytes between exec path and argv[0]).
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}

	// Skip argc argv entries.
	for i := uint32(0); i < argc; i++ {
		n := bytes.IndexByte(rest, 0)
		if n < 0 {
			return nil, fmt.Errorf("procargs2: truncated argv at index %d", i)
		}
		rest = rest[n+1:]
	}

	// Remaining null-terminated strings are the env, ending at empty entry or EOF.
	var env []string
	for len(rest) > 0 {
		n := bytes.IndexByte(rest, 0)
		if n < 0 {
			env = append(env, string(rest))
			break
		}
		if n == 0 {
			break
		}
		env = append(env, string(rest[:n]))
		rest = rest[n+1:]
	}
	return env, nil
}
