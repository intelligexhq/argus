package collector

import (
	"context"
	"testing"
	"time"

	"github.com/intelligexhq/argus/internal/classify"
)

func TestEnvCollector_Name(t *testing.T) {
	if got := NewEnvCollector(classify.NewDefault()).Name(); got != "env" {
		t.Errorf("Name() = %q, want %q", got, "env")
	}
}

func TestParseEndpointURL(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort uint32
	}{
		{"http://gpu.internal:11434", "gpu.internal", 11434},
		{"https://api.example.com/v1", "api.example.com", 0},
		{"gpu.internal:11434", "gpu.internal", 11434},
		{"gpu.internal", "gpu.internal", 0},
		{"10.0.0.5:8000", "10.0.0.5", 8000},
		{"[::1]:8080", "::1", 8080},
		{"", "", 0},
		{"   ", "", 0},
	}
	for _, tc := range cases {
		h, p := parseEndpointURL(tc.in)
		if h != tc.wantHost || p != tc.wantPort {
			t.Errorf("parseEndpointURL(%q) = (%q,%d), want (%q,%d)",
				tc.in, h, p, tc.wantHost, tc.wantPort)
		}
	}
}

func TestEnvCollector_ScanEnv(t *testing.T) {
	c := NewEnvCollector(classify.NewDefault())
	now := time.Unix(0, 0)
	env := []string{
		"PATH=/usr/bin:/bin",
		"OLLAMA_HOST=http://gpu.internal:11434",
		"OPENAI_BASE_URL=https://api.openai.com/v1",
		"ANTHROPIC_BASE_URL=", // empty value — must be skipped
		"NOT_A_KEY",           // malformed — must be skipped
		"AZURE_OPENAI_ENDPOINT=https://my-resource.openai.azure.com",
	}
	got := c.scanEnv(env, 1234, now)
	if len(got) != 3 {
		t.Fatalf("want 3 synthetic connections, got %d (%+v)", len(got), got)
	}

	byEnv := map[string]int{}
	for _, c := range got {
		byEnv[c.SourceDetail]++
		if c.PID != 1234 {
			t.Errorf("PID not propagated: got %d", c.PID)
		}
		if c.Source != "env" {
			t.Errorf("Source = %q, want env", c.Source)
		}
		if !c.ObservedAt.Equal(now) {
			t.Errorf("ObservedAt mismatch on %+v", c)
		}
		if c.Endpoint == "" {
			t.Errorf("missing endpoint label on %+v", c)
		}
	}
	for _, want := range []string{"OLLAMA_HOST", "OPENAI_BASE_URL", "AZURE_OPENAI_ENDPOINT"} {
		if byEnv[want] != 1 {
			t.Errorf("expected exactly one row from %s, got %d", want, byEnv[want])
		}
	}

	// Locality propagated through ClassifyLocality; hostnames go into
	// RemoteHost (not RemoteIP), which stays empty for these inputs.
	for _, c := range got {
		switch c.SourceDetail {
		case "OLLAMA_HOST":
			if c.Classification != "private" || c.RemoteHost != "gpu.internal" || c.RemoteIP != "" || c.RemotePort != 11434 {
				t.Errorf("OLLAMA_HOST parse wrong: %+v", c)
			}
		case "OPENAI_BASE_URL":
			if c.Classification != "public" || c.RemoteHost != "api.openai.com" || c.RemoteIP != "" {
				t.Errorf("OPENAI_BASE_URL parse wrong: %+v", c)
			}
		}
	}
}

func TestEnvCollector_ScanEnv_IPLiteralGoesToRemoteIP(t *testing.T) {
	// When the env URL contains a literal IP, it should populate RemoteIP and
	// leave RemoteHost empty — opposite of the hostname case above.
	c := NewEnvCollector(classify.NewDefault())
	got := c.scanEnv([]string{"OLLAMA_HOST=http://10.0.0.5:11434"}, 1, time.Unix(0, 0))
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].RemoteIP != "10.0.0.5" || got[0].RemoteHost != "" {
		t.Errorf("IP literal should populate RemoteIP, got %+v", got[0])
	}
	if got[0].Classification != "private" {
		t.Errorf("10.0.0.5 should be private, got %q", got[0].Classification)
	}
}

func TestEnvCollector_CollectFilterContract(t *testing.T) {
	// Smoke test against the live OS. The test process likely has no model env
	// vars set, so we don't assert on count — only the per-row contract.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := NewEnvCollector(classify.NewDefault()).Collect(ctx)
	if err != nil {
		t.Skipf("env collector unavailable on this host (%v) — skipping", err)
	}
	for _, c := range res.Connections {
		if c.Source != "env" {
			t.Errorf("Source = %q, want env: %+v", c.Source, c)
		}
		if c.SourceDetail == "" {
			t.Errorf("SourceDetail empty on env row: %+v", c)
		}
		if c.Endpoint == "" {
			t.Errorf("Endpoint label empty: %+v", c)
		}
		if c.PID == 0 {
			t.Errorf("PID zero: %+v", c)
		}
	}
}
