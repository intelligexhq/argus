package classify

import (
	"context"
	"net"
	"testing"
)

func TestClassify_Locality(t *testing.T) {
	c := NewDefault()
	cases := []struct {
		ip, want string
	}{
		{"127.0.0.1", "loopback"},
		{"::1", "loopback"},
		{"10.0.0.5", "private"},
		{"172.16.0.1", "private"},
		{"192.168.1.1", "private"},
		{"fc00::1", "private"},
		{"8.8.8.8", "public"},
		{"1.1.1.1", "public"},
		{"not-an-ip", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		c.cache[tc.ip] = rdnsResult{} // prime to skip net.LookupAddr (offline tests)
		_, _, got := c.Classify(t.Context(), tc.ip)
		if got != tc.want {
			t.Errorf("Classify(%q) class = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

func TestClassify_ReturnsRawPTRHost(t *testing.T) {
	// Even when no label matches, the raw PTR result is returned for callers
	// to persist as remote_host. Prime cache with a synthesised entry to keep
	// the test offline.
	c := NewDefault()
	c.cache["104.16.0.1"] = rdnsResult{label: "", host: "1.0.16.104.cloudflareaccess.com"}
	label, host, _ := c.Classify(t.Context(), "104.16.0.1")
	if label != "" {
		t.Errorf("label = %q, want empty (no allowlist match)", label)
	}
	if host != "1.0.16.104.cloudflareaccess.com" {
		t.Errorf("host = %q, want raw PTR result", host)
	}
}

func TestIsPrivate(t *testing.T) {
	c := NewDefault()
	private := []string{"10.0.0.5", "172.16.0.1", "172.31.255.254", "192.168.1.1", "fc00::1"}
	public := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "172.32.0.1"}
	for _, ip := range private {
		if !c.isPrivate(net.ParseIP(ip)) {
			t.Errorf("isPrivate(%s) = false, want true", ip)
		}
	}
	for _, ip := range public {
		if c.isPrivate(net.ParseIP(ip)) {
			t.Errorf("isPrivate(%s) = true, want false", ip)
		}
	}
}

func TestRegisterModelHost_CaseFolded(t *testing.T) {
	c := NewDefault()
	c.RegisterModelHost("VLLM.INTERNAL", "Private vLLM")
	if got := c.modelHosts["vllm.internal"]; got != "Private vLLM" {
		t.Errorf("RegisterModelHost did not case-fold; modelHosts[vllm.internal] = %q", got)
	}
}

func TestLookupEnvVar(t *testing.T) {
	c := NewDefault()
	if l, ok := c.LookupEnvVar("OLLAMA_HOST"); !ok || l != "Ollama" {
		t.Errorf("LookupEnvVar(OLLAMA_HOST) = (%q, %v), want (Ollama, true)", l, ok)
	}
	if l, ok := c.LookupEnvVar("ollama_host"); !ok || l != "Ollama" {
		t.Errorf("LookupEnvVar must be case-insensitive; got (%q, %v)", l, ok)
	}
	if _, ok := c.LookupEnvVar("PATH"); ok {
		t.Errorf("LookupEnvVar(PATH) should not match")
	}
}

func TestRegisterEndpointEnv(t *testing.T) {
	c := NewDefault()
	c.RegisterEndpointEnv("my_proxy_url", "Internal Proxy")
	if l, ok := c.LookupEnvVar("MY_PROXY_URL"); !ok || l != "Internal Proxy" {
		t.Errorf("RegisterEndpointEnv round-trip failed: got (%q, %v)", l, ok)
	}
}

func TestLookupRDNS_CancelledContextSkipsCache(t *testing.T) {
	// A cancelled ctx must return empty results AND leave the cache untouched,
	// so the next scan retries instead of perpetually treating the IP as
	// "looked up, nothing useful".
	c := NewDefault()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const ip = "8.8.8.8"
	label, host, locality := c.Classify(ctx, ip)
	if label != "" || host != "" {
		t.Errorf("cancelled ctx should return empty label/host, got label=%q host=%q", label, host)
	}
	if locality != "public" {
		t.Errorf("locality is offline (no DNS), should still be %q, got %q", "public", locality)
	}

	c.mu.Lock()
	_, cached := c.cache[ip]
	c.mu.Unlock()
	if cached {
		t.Error("cache was populated despite ctx cancellation; future scans will not retry")
	}
}

func TestClassifyLocality(t *testing.T) {
	c := NewDefault()
	cases := []struct {
		host, want string
	}{
		// IPs go through CIDR membership
		{"127.0.0.1", "loopback"},
		{"::1", "loopback"},
		{"10.0.0.5", "private"},
		{"fc00::1", "private"},
		{"8.8.8.8", "public"},
		// hostnames via suffix heuristics, no DNS
		{"localhost", "loopback"},
		{"gpu.internal", "private"},
		{"node1.local", "private"},
		{"box.lan", "private"},
		{"api.example.com", "public"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		if got := c.ClassifyLocality(tc.host); got != tc.want {
			t.Errorf("ClassifyLocality(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}
