// Package classify decides what a remote endpoint is: its network locality
// (public / private / loopback) and, best-effort, whether it is a known model
// service. Endpoints are deliberately data-driven rather than hardcoded so an
// operator can register private inference hosts (internal vLLM/Triton, a
// VPC-private Bedrock/Azure endpoint) as model endpoints.
package classify

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// rdnsResolver is forced to the pure-Go path: the cgo getaddrinfo resolver
// ignores context cancellation, so a stalled DNS server can leak goroutines
// even after the per-lookup deadline fires. CGO is off in our build today, but
// this is cheap insurance.
var rdnsResolver = &net.Resolver{PreferGo: true}

// rdnsLookupTimeout caps each individual reverse-DNS call. Results are cached
// for the process lifetime so this only affects cold lookups.
const rdnsLookupTimeout = 2 * time.Second

// EndpointClassifier resolves remote IPs to (label, classification).
type EndpointClassifier struct {
	// modelHosts maps a hostname substring to a human label. Reverse-DNS of the
	// remote IP is matched against these.
	modelHosts map[string]string
	// endpointEnvs maps an env-var name (case-insensitive) to a model-service
	// label, for the env-var collector.
	endpointEnvs map[string]string
	privateNets  []*net.IPNet

	mu    sync.Mutex
	cache map[string]rdnsResult // ip -> resolved (label, host)
}

// rdnsResult holds the cached output of a reverse-DNS lookup: the curated
// model-service label (empty if no match against modelHosts) and the first
// PTR hostname (empty if the lookup failed).
type rdnsResult struct {
	label string
	host  string
}

// NewDefault seeds the well-known public model endpoints and RFC1918/ULA ranges.
func NewDefault() *EndpointClassifier {
	ec := &EndpointClassifier{
		modelHosts: map[string]string{
			"api.anthropic.com":                 "Anthropic API",
			"api.openai.com":                    "OpenAI API",
			"openai.azure.com":                  "Azure OpenAI",
			"generativelanguage.googleapis.com": "Google Gemini API",
			"bedrock":                           "AWS Bedrock",
			"api.cohere.ai":                     "Cohere API",
			"api.mistral.ai":                    "Mistral API",
			"api.groq.com":                      "Groq API",
		},
		endpointEnvs: map[string]string{
			"OPENAI_BASE_URL":       "OpenAI",
			"OPENAI_API_BASE":       "OpenAI",
			"ANTHROPIC_BASE_URL":    "Anthropic",
			"ANTHROPIC_API_URL":     "Anthropic",
			"AZURE_OPENAI_ENDPOINT": "Azure OpenAI",
			"OLLAMA_HOST":           "Ollama",
			"GROQ_API_BASE":         "Groq",
			"MISTRAL_BASE_URL":      "Mistral",
			"GOOGLE_GENAI_API_BASE": "Google Gemini",
			"COHERE_API_URL":        "Cohere",
		},
		cache: map[string]rdnsResult{},
	}
	for _, cidr := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
	} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			ec.privateNets = append(ec.privateNets, n)
		}
	}
	return ec
}

// RegisterModelHost adds a private/custom endpoint so its traffic is labelled as
// a model service. (Wired to config in a later iteration.)
func (c *EndpointClassifier) RegisterModelHost(hostSubstring, label string) {
	c.modelHosts[strings.ToLower(hostSubstring)] = label
}

// RegisterEndpointEnv adds an env-var name whose value points at a model
// service. Used by the env collector to label process-declared endpoints.
func (c *EndpointClassifier) RegisterEndpointEnv(envName, label string) {
	c.endpointEnvs[strings.ToUpper(envName)] = label
}

// LookupEnvVar returns the label registered for an env var name, if any. Match
// is case-insensitive so callers don't need to normalise.
func (c *EndpointClassifier) LookupEnvVar(envName string) (string, bool) {
	l, ok := c.endpointEnvs[strings.ToUpper(envName)]
	return l, ok
}

// ClassifyLocality returns the network locality of a host without DNS lookups.
// IPs use CIDR membership; hostnames use suffix heuristics (.local / .internal
// / .lan, plus "localhost"). Returns "loopback" | "private" | "public" | "unknown".
func (c *EndpointClassifier) ClassifyLocality(host string) string {
	if host == "" {
		return "unknown"
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsLoopback():
			return "loopback"
		case c.isPrivate(ip):
			return "private"
		default:
			return "public"
		}
	}
	h := strings.ToLower(host)
	switch {
	case h == "localhost":
		return "loopback"
	case strings.HasSuffix(h, ".local"),
		strings.HasSuffix(h, ".internal"),
		strings.HasSuffix(h, ".lan"):
		return "private"
	default:
		return "public"
	}
}

// Classify returns (modelLabel, rDNS host, locality) for a remote IP.
// modelLabel is "" when the endpoint is not a recognised model service. host is
// the raw PTR result (possibly generic, e.g. "*.cloudfront.net") — preserved so
// callers can record provenance even when no curated label matched. host is ""
// if rDNS lookup failed or returned nothing. The ctx caps the reverse-DNS
// lookup; a cancelled ctx returns ("", "", locality) without poisoning the cache.
func (c *EndpointClassifier) Classify(ctx context.Context, ipStr string) (label, host, locality string) {
	locality = "unknown"
	if ip := net.ParseIP(ipStr); ip != nil {
		switch {
		case ip.IsLoopback():
			locality = "loopback"
		case c.isPrivate(ip):
			locality = "private"
		default:
			locality = "public"
		}
	}
	label, host = c.lookupRDNS(ctx, ipStr)
	return
}

func (c *EndpointClassifier) isPrivate(ip net.IP) bool {
	for _, n := range c.privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// lookupRDNS performs a reverse-DNS lookup and returns (curated label, raw host).
// The result is cached; an entry with empty fields means "looked up, nothing
// useful" — definitive DNS failures (NXDOMAIN etc.) cache an empty entry so we
// don't retry. Context cancellation/timeout does NOT cache, so a slow DNS path
// gets another chance on the next scan.
func (c *EndpointClassifier) lookupRDNS(ctx context.Context, ipStr string) (label, host string) {
	c.mu.Lock()
	if entry, ok := c.cache[ipStr]; ok {
		c.mu.Unlock()
		return entry.label, entry.host
	}
	c.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, rdnsLookupTimeout)
	defer cancel()
	names, err := rdnsResolver.LookupAddr(lctx, ipStr)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", ""
	}
	if err == nil && len(names) > 0 {
		host = strings.TrimSuffix(strings.ToLower(names[0]), ".")
		for _, name := range names {
			n := strings.ToLower(name)
			for sub, l := range c.modelHosts {
				if strings.Contains(n, sub) {
					label = l
					break
				}
			}
			if label != "" {
				break
			}
		}
	}

	c.mu.Lock()
	c.cache[ipStr] = rdnsResult{label: label, host: host}
	c.mu.Unlock()
	return
}
