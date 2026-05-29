package collector

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/intelligexhq/argus/internal/classify"
	"github.com/intelligexhq/argus/internal/model"
	"github.com/shirou/gopsutil/v3/process"
)

// EnvCollector reads process environment for well-known model-endpoint env vars
// (OPENAI_BASE_URL, OLLAMA_HOST, AZURE_OPENAI_ENDPOINT, …) and emits synthetic
// Connection rows tagged source="env". This catches agents that announce their
// endpoint via configuration (LiteLLM proxies, self-hosted Ollama, Azure
// OpenAI, etc.) without needing packet capture, complementing rDNS-based
// labelling on the socket collector.
//
// Visibility is per current-user scope: reading another user's process env
// fails by design; we skip those silently. The per-PID env read is platform-
// split (see env_darwin.go / env_other.go) because gopsutil's Environ() is
// not implemented on macOS.
type EnvCollector struct {
	classifier *classify.EndpointClassifier
}

func NewEnvCollector(c *classify.EndpointClassifier) *EnvCollector {
	return &EnvCollector{classifier: c}
}

func (c *EnvCollector) Name() string { return "env" }

func (c *EnvCollector) Collect(ctx context.Context) (Result, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return Result{}, err
	}
	now := time.Now()
	var out []model.Connection
	for _, p := range procs {
		if ctx.Err() != nil {
			// Return partial results — the rows we already gathered are still
			// useful signal. Caller's next loop iteration will respect ctx.
			return Result{Connections: out}, nil
		}
		env, err := readProcessEnv(ctx, p.Pid)
		if err != nil {
			continue // protected / other-user process
		}
		out = append(out, c.scanEnv(env, p.Pid, now)...)
	}
	return Result{Connections: out}, nil
}

// scanEnv extracts synthetic Connection rows from a single process's env. Split
// out so the per-entry parsing is unit-testable without spawning a subprocess.
func (c *EnvCollector) scanEnv(env []string, pid int32, now time.Time) []model.Connection {
	var out []model.Connection
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		label, ok := c.classifier.LookupEnvVar(key)
		if !ok || val == "" {
			continue
		}
		host, port := parseEndpointURL(val)
		var ip, hostname string
		if net.ParseIP(host) != nil {
			ip = host
		} else {
			hostname = host
		}
		out = append(out, model.Connection{
			PID:            pid,
			RemoteIP:       ip,
			RemoteHost:     hostname,
			RemotePort:     port,
			Endpoint:       label,
			Classification: c.classifier.ClassifyLocality(host),
			ObservedAt:     now,
			Source:         "env",
			SourceDetail:   key,
		})
	}
	return out
}

// parseEndpointURL extracts host/port from common endpoint formats:
//
//	https://api.example.com/v1   → (api.example.com, 0)
//	http://gpu.internal:11434    → (gpu.internal, 11434)
//	gpu.internal:11434           → (gpu.internal, 11434)
//	[::1]:8080                   → (::1, 8080)
//	gpu.internal                 → (gpu.internal, 0)
//
// Returns ("", 0) on unparseable input.
func parseEndpointURL(raw string) (string, uint32) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", 0
		}
		return u.Hostname(), parsePort(u.Port())
	}
	if h, p, err := net.SplitHostPort(raw); err == nil {
		return h, parsePort(p)
	}
	return raw, 0
}

func parsePort(s string) uint32 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}
