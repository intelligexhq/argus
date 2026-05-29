//go:build darwin

package collector

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// buildProcargs2 fabricates a buffer in the KERN_PROCARGS2 layout for parser testing.
func buildProcargs2(execPath string, padding int, argv, env []string) []byte {
	var b bytes.Buffer
	// argc (little-endian uint32)
	argc := uint32(len(argv))
	b.WriteByte(byte(argc))
	b.WriteByte(byte(argc >> 8))
	b.WriteByte(byte(argc >> 16))
	b.WriteByte(byte(argc >> 24))
	// exec path + terminator
	b.WriteString(execPath)
	b.WriteByte(0)
	// alignment padding
	for i := 0; i < padding; i++ {
		b.WriteByte(0)
	}
	// argv strings
	for _, a := range argv {
		b.WriteString(a)
		b.WriteByte(0)
	}
	// env strings
	for _, e := range env {
		b.WriteString(e)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func TestParseProcargs2(t *testing.T) {
	buf := buildProcargs2(
		"/usr/local/bin/foo",
		3, // padding bytes
		[]string{"foo", "--flag", "value"},
		[]string{"PATH=/usr/bin", "OLLAMA_HOST=http://gpu.internal:11434", "EMPTY="},
	)
	got, err := parseProcargs2(buf)
	if err != nil {
		t.Fatalf("parseProcargs2: %v", err)
	}
	want := []string{"PATH=/usr/bin", "OLLAMA_HOST=http://gpu.internal:11434", "EMPTY="}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseProcargs2_NoPadding(t *testing.T) {
	buf := buildProcargs2("/bin/x", 0, []string{"x"}, []string{"K=V"})
	got, err := parseProcargs2(buf)
	if err != nil {
		t.Fatalf("parseProcargs2: %v", err)
	}
	if len(got) != 1 || got[0] != "K=V" {
		t.Errorf("got %v, want [K=V]", got)
	}
}

func TestParseProcargs2_TooSmall(t *testing.T) {
	if _, err := parseProcargs2([]byte{0, 0}); err == nil {
		t.Error("expected error for buffer < 4 bytes")
	}
}

func TestParseProcargs2_TruncatedArgv(t *testing.T) {
	// argc=2 but only one argv string before EOF.
	buf := []byte{2, 0, 0, 0, '/', 'b', 0, 0, 'a', 'r', 'g', 0}
	if _, err := parseProcargs2(buf); err == nil {
		t.Error("expected error for truncated argv")
	}
}

func TestReadProcessEnv_SelfRoundtrip(t *testing.T) {
	// End-to-end on Darwin: read our own process env via sysctl and verify the
	// PATH variable shows up (it's set in every test runner environment).
	env, err := readProcessEnv(context.Background(), int32(os.Getpid()))
	if err != nil {
		t.Fatalf("readProcessEnv: %v", err)
	}
	if len(env) == 0 {
		t.Fatal("expected non-empty env from own process")
	}
	found := false
	for _, kv := range env {
		if len(kv) >= 5 && kv[:5] == "PATH=" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("PATH not found in own env (read %d entries)", len(env))
	}
}
