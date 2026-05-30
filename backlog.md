# Argus Backlog

Argus answers the question "what AI is running on this machine and what's it doing" in 30 seconds, from a clean install, without root, without trust ceremonies. 

## Non-goals

These are explicit out-of-scope decisions, not gaps. They shape what argus *is* by saying what it isn't.

- **No payload capture / no TLS interception.** Argus does not sit in the data path between an agent and its remote endpoints. We do not run a local proxy, do not install a CA cert, do not read decrypted request/response bodies. Audit-grade payload visibility is a different product category (EDR / DLP / cooperative SDK) and belongs in a separate tool if we ever build it.
- **No root / no kernel modules.** Everything argus does runs as the invoking user. No eBPF, no kernel extensions, no signed entitlements. The day argus needs root is the day it stops being argus.
- **No agent modification.** Argus observes; it does not patch, instrument, or wrap the AI tooling it discovers. SDK-cooperative observability (Helicone, Langsmith) is a different posture.
- **No behavioural analysis or anomaly detection.** Argus surfaces facts ("this agent talked to this endpoint at this time, this many bytes"). Inference about intent, threat, or anomaly is the consumer's job.

## Engineering hygiene

From the internal Go-ecosystem review. No user-facing behaviour changes.

### Open, ordered by impact

1. **Data race on `modelHosts` / `endpointEnvs`** in `internal/classify/classify.go:75-83`. The maps are mutated without holding the existing `mu` despite being read concurrently from scan goroutines. `RegisterModelHost` is documented as "wired to config in a later iteration", so the race is latent today — fix it before that lands. Options: lock writes against `mu`, or split into a setup-time-immutable map and a runtime-locked overrides map.
2. **Store abstraction in `internal/api`**. Handlers depend on `*store.Store` concretely, so handler unit tests need on-disk SQLite. Introduce a small `Reader` interface in the `api` package listing only the methods handlers use (`ListAgents`, `ListProcesses`, `ListConnections`) and have `NewServer` accept it. Enables fake-store handler tests.
3. **Prepared statements + `LIMIT`**. `WriteSnapshot` re-parses the same INSERT N times — prepare once per scan. `/v1/processes` and `/v1/connections` return unbounded JSON — add `?limit=N` with a sane default cap and an OpenAPI note.
4. **Dependency hygiene**. Bump `gopsutil` v3 → v4 (v3 is in maintenance). Add `govulncheck` to the CI workflow.
5. **`golangci-lint`**. Add `.golangci.yml` enabling `errcheck`, `gocritic`, `gosec`, `revive`, `staticcheck`. Wire into CI. Will flag the items in "minor nits" below automatically.
6. **`LICENSE` + version injection**. Choose a license (MIT or Apache-2.0 for a tool that may be vendored). Inject build info with `-ldflags "-X main.version=$(git describe) -X main.commit=$(git rev-parse --short HEAD)"` and expose at `GET /v1/version`.

### Minor nits

- `crypto/sha1` → `crypto/sha256` in `internal/correlate/correlate.go:59` (linter bait, not security-critical here — IDs only).
- `gopsutil` `*WithContext` errors silently discarded in `internal/collector/process.go:33-36`; log at debug instead so triage of "missing process" rows is possible.
- `internal/api/server.go:48` — `json.Encode` error swallowed; log at debug.
- `internal/store/store.go:91` — wrap `tx.Rollback` to filter `sql.ErrTxDone` so successful commits don't log noisy rollback errors on some drivers.
- `.gitignore` add: `dist/`, `coverage.out`, `*.test`, `.idea/`, `.vscode/`.
- `go.mod` pins `go 1.26.3`; readme says "Go 1.26+". Pick a true minimum and reconcile.
- Readme typos in current copy: "mashine", "architechture", "caped", "testig".
- Benchmarks (`BenchmarkParseProcargs2`) and fuzz targets (`FuzzParseProcargs2`, `FuzzParseEndpointURL`) — `parseProcargs2` consumes a raw kernel buffer; prime fuzz candidate.

## Feature: activity capture

Goal: surface what agents are *doing over time*, not just the current snapshot. Today the store overwrites the process and connection tables on each scan, and `scan_runs` only keeps counters — there is no way to answer "what endpoints did claude-code talk to in the last hour" or "when did this agent first appear".

### Phase 1 — Per-scan history + retention

**Status**: design locked, ready to implement.

**Schema** (additive, `CREATE TABLE IF NOT EXISTS` — real migration runner deferred to Phase 2):

```sql
ALTER TABLE scan_runs ADD COLUMN prev_scan_id INTEGER REFERENCES scan_runs(id);

CREATE TABLE process_observations (
  scan_id      INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
  pid          INTEGER NOT NULL,
  ppid         INTEGER NOT NULL,
  started_at   INTEGER NOT NULL,        -- ms; part of identity (PID recycles)
  name         TEXT,
  exe          TEXT,
  cmdline      TEXT,
  agent_id     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (scan_id, pid, started_at)
);
CREATE INDEX idx_proc_obs_agent_scan ON process_observations(agent_id, scan_id);
CREATE INDEX idx_proc_obs_identity   ON process_observations(pid, started_at);

CREATE TABLE connection_observations (
  scan_id        INTEGER NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
  pid            INTEGER NOT NULL,
  started_at     INTEGER NOT NULL,
  remote_ip      TEXT,
  remote_host    TEXT NOT NULL DEFAULT '',
  remote_port    INTEGER,
  endpoint       TEXT,
  classification TEXT,
  agent_id       TEXT NOT NULL DEFAULT '',
  source         TEXT NOT NULL DEFAULT 'socket',
  source_detail  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_conn_obs_agent_scan ON connection_observations(agent_id, scan_id);
CREATE INDEX idx_conn_obs_endpoint   ON connection_observations(endpoint) WHERE endpoint <> '';
```

`processes` / `connections` (current-snapshot tables) stay as-is; `/v1/processes`, `/v1/connections`, `/v1/agents` keep working. Observation tables are append-only per scan.

**Writer flow** (`WriteSnapshot`):
1. `BEGIN` tx.
2. Insert `scan_runs` row with `prev_scan_id = max(scan_runs.id)`; returns `scan_id`.
3. Upsert agents (unchanged).
4. Replace `processes` table; also append to `process_observations` with `scan_id`. Prepared statements once per scan.
5. Replace `connections` table; also append to `connection_observations`.
6. `COMMIT`.

**Retention**:
- Default `7d`, flag `--retention <duration>`.
- Background goroutine in `main`: prune once at startup, then once per hour.
- Mechanism: `DELETE FROM scan_runs WHERE started_at < ?` — FK cascade drops observation rows.
- Chunked at 10k rows/batch to avoid long write txns on first prune of a stalled instance.
- No automatic `VACUUM`; revisit if growth becomes a problem.

**API additions**:
- `GET /v1/agents/{id}/timeline?from=<RFC3339>&to=<RFC3339>&bucket=1m|5m|1h`
  - Defaults: last 1h; bucket auto-picked from window (`≤1h → 1m`, `≤24h → 5m`, `>24h → 1h`).
  - Response: `{agent_id, from, to, bucket, buckets:[{start, n_scans, n_processes, n_connections, n_endpoints}]}`.
  - `n_processes` = distinct `(pid, started_at)`; `n_connections` = distinct `(remote_ip, remote_port, endpoint)`; `n_endpoints` = distinct non-empty endpoint labels; `n_scans` lets clients spot scan gaps.
- `GET /v1/agents/{id}/endpoints`
  - Returns the distinct endpoints this agent has ever talked to: `[{endpoint, classification, remote_host, first_seen, last_seen, n_observations}]`.
  - The single most useful "inspect what's it doing" answer.

**Storage estimate**: 15s scan × 5760 scans/day × ~25 obs/scan × ~100 B/row × 7d ≈ ~100 MB/week typical; ~600 MB/week on a busy host (100 procs + 50 conns). Documented in readme.

**Implementation order**:
1. Schema additions + `WriteSnapshot` dual-write with prepared statements (covers backlog item "prepared statements" too).
2. Retention pruner goroutine in `main`.
3. `store.ListTimeline(ctx, agentID, from, to, bucket)` + handler + OpenAPI entry.
4. `store.ListAgentEndpoints(ctx, agentID)` + handler + OpenAPI entry.
5. Tests at each step.
6. Readme + backlog flip Phase 1 to "shipped".

### Phase 2 — Events from snapshot diffs

- Derive events by diffing each new scan against the previous one: `process_started`, `process_exited`, `connection_opened`, `connection_closed`, `endpoint_first_seen`.
- New table `agent_events(id, agent_id, type, payload_json, occurred_at)`. Derivation runs on the writer.
- API: `GET /v1/agents/{id}/events?from=<t>&to=<t>&type=...`.
- Edge cases to design for: PID recycling (PID + start_time is the real identity), scan gaps > 2× interval (mark as `gap` event so downstream consumers don't infer false transitions), short-lived child procs the snapshot misses (out of scope until the event-collector backlog item lands).

### Phase 3 — Rollups and rates

- Pre-computed hourly rollups for dashboard views.
- `GET /v1/agents/{id}/activity?window=1h|24h|7d` returns summary card data: connection count, unique endpoints, peak processes, percent-time-active.
- Optional Prometheus exporter at `/metrics` (per-agent counters, scan duration histogram).

### Open questions

- Activity needs PID+start_time as identity, not bare PID. Confirm `gopsutil` exposes `CreateTime` reliably across platforms (it does on Linux/macOS; verify Windows).
- Should we run a faster "lite" scan (process-only, every 5s) in parallel with the full 15s scan to tighten process_started resolution, or wait for the event-collector?
- Cardinality: an agent that talks to many distinct ephemeral endpoints can blow up `endpoint_first_seen` events. Bucket or cap?

## Feature: deeper passive signals

The B-path extension to the activity story. Each signal stays within the **no root / no interception** non-goals and adds depth to the "what is it doing" half of the product question. The four signals are ordered by implementation cost — cheapest first — because they have very different effort profiles and we should ship in that order.

### Model-variant extraction (cheapest, mostly free)

- Parse model name from the data we already collect: command-line arguments (`--model claude-3-5-sonnet`, `--model-id gpt-4o`), env vars (`OLLAMA_MODEL`, `ANTHROPIC_MODEL`), exe path.
- Output: `model` field on `Process` / `Agent` when confidently inferred, with a `model_source` indicating cmdline / env / exe-name.
- Cross-platform: trivially. The process collector already has cmdline + env.
- Effort: ~1 day. Mostly heuristic table maintenance.

### Per-connection byte counters (medium effort, Linux first)

- Track `bytes_in` / `bytes_out` / `last_active_at` per socket across scans (not just present/absent like today).
- Linux: read `/proc/net/tcp{,6}` and the netlink socket diag API for per-socket counters. Already partly accessible via gopsutil's network IO functions; per-connection (not per-interface) needs the `SOCK_DIAG_BY_FAMILY` netlink request.
- macOS: harder — no direct equivalent of `/proc/net/tcp` for byte counters. Options: parse `nettop` output, use the `proc_pidinfo` API for socket info (no byte counters), or accept Linux-only for v1.
- Windows: `GetExtendedTcpTable` / `IPHLPAPI` — byte counters not directly available; deferred.
- Effort: ~3-5 days Linux, indeterminate macOS. Worth a research spike before committing to cross-platform.

### DNS query observation (hard, platform-specific)

- Catches the real hostname (e.g. `api.anthropic.com`) before the connection lands on a CDN IP whose rDNS is generic.
- Options ranked by feasibility within non-goals:
  - **Process DNS state inspection**: read which DNS queries the agent's resolver library has cached. No portable API; not reliable.
  - **Localhost DNS sniffing**: observe traffic to port 53 on lo. On Linux, this needs `CAP_NET_RAW` — borderline-violates "no root" unless the user grants the cap explicitly. On macOS, generally needs admin.
  - **Resolver-library cooperative path**: if systemd-resolved or mDNSResponder exposes a query log, read it. Linux systemd-resolved does; macOS doesn't usefully.
- Effort: 1-2 weeks if done well, with platform-specific code per OS.
- Status: **risk of violating non-goals**. Flag for discussion before committing — may be the wrong signal for argus and better answered by env-var observation we already do.

### JA3 TLS fingerprint (most expensive, may not fit non-goals)

- Identifies the SDK / HTTP client behind a connection by hashing the TLS ClientHello (cipher suite list, extensions, curves).
- Capture requires raw-socket access or BPF — both need root or `CAP_NET_RAW` on Linux, special entitlements on macOS.
- Effort: 1+ week for the collector, plus a JA3 → SDK lookup table to maintain.
- Status: **conflicts with non-goals as currently scoped.** Including this requires either (a) accepting a "JA3 mode" that asks the user for `CAP_NET_RAW`, or (b) cutting the signal. Recommend deferring and revisiting after the cheaper signals ship.

### Open question

The four signals are not equal-effort. Implementation order should be: model-variant → byte counters (Linux first) → DNS observation (research spike first) → JA3 (decide whether non-goals allow it). Confirm this order is acceptable, or which signals to cut if non-goals must hold strictly.

## Collection depth (deferred per non-goals)

These items appeared in earlier roadmap drafts and are now explicitly **deferred** by the non-goals above. Kept here so the decision is visible to future readers.

- ~~Event collector (eBPF / EndpointSecurity / ETW) for short-lived subagents~~ — requires kernel-level integration; out of scope.
- ~~Optional privileged mode for system-wide visibility~~ — argus runs as the invoking user; out of scope.

## API & ops

- **Token auth** for deployments that bind beyond loopback. Default loopback / unix-socket posture stays auth-free.
- **Embed Swagger UI assets** in the binary instead of loading from `unpkg.com`, so `/v1/openapi` works fully offline.
- **CORS** — likely needed once a real web UI exists; out of scope for the loopback-only API today.
- **`/v1/version`** endpoint returning `{version, commit, go_version, started_at}` (couples with the `-ldflags` work above).

## Distribution

User starts with `git clone && make build` today. Distribution tiers, ordered by lift vs. reach:

### Tier 1 — Source build (today)

```
git clone https://github.com/intelligexhq/argus
cd argus
make build
```

Requires Go 1.26+ on the user's machine. Good enough for early adopters and CI.

### Tier 2 — GitHub Releases binaries

- Tool: `goreleaser` (driven from a GitHub Actions release job triggered by `vX.Y.Z` tags).
- Targets: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. Add `windows/amd64` once the Windows collector path is exercised.
- Artifacts: `.tar.gz` archives + `SHA256SUMS`. Stretch: Cosign signatures.
- Install: `curl -L .../argus_darwin_arm64.tar.gz | tar xz && mv argus /usr/local/bin/`.

### Tier 3 — `go install`

- `go install github.com/intelligexhq/argus/cmd/argus@latest`.
- Free with a public repo and a tag; module proxy + checksum DB do the rest.
- Audience: Go developers who already have the toolchain.

### Tier 4 — Homebrew tap

- `brew install intelligexhq/tap/argus`.
- New repo `intelligexhq/homebrew-tap` with `Formula/argus.rb` pointing at the release tarball + SHA. `goreleaser` can update the formula on every tag.
- Audience: macOS developers (likely the early-adopter shape of this tool).
- Future: submit to homebrew-core once stable.

### Tier 5 — Container image

- `ghcr.io/intelligexhq/argus:vX.Y.Z` and `:latest`. Build via the same release pipeline.
- Base: `gcr.io/distroless/static` — the binary is CGO-free, so the image is ~10 MB.
- Caveat: containers see a namespaced view of `/proc` and `/sys`. To observe the host, the user has to mount them in (`-v /proc:/host/proc:ro -v /sys:/host/sys:ro`) and Argus needs to be taught the alternate roots — track this as a separate backlog item under "Container support".

### Stretch — Native Linux packages

- `nfpm` (also driven by goreleaser) produces `.deb` and `.rpm`.
- Ships a `systemd` unit (`argus.service`) that runs the daemon under a dedicated unprivileged user, listening on a unix socket under `/run/argus/`.
- Defer until there's clear Linux server demand.

### Stretch — One-liner installer

- `curl -sSf https://argus.intelligex.dev/install.sh | sh` (rustup-style).
- Script detects OS / arch, downloads the matching release archive, drops into `/usr/local/bin`.
- Requires a static host for the script. Defer until releases exist.

### Recommended order

1. **Now**: source build only (already works).
2. **At v0.1 tag**: GitHub Releases binaries + `go install` (same release infra; both unlock the same day).
3. **Soon after**: Homebrew tap.
4. **When Linux usage shows up**: container image, then native packages.
5. **When stable**: one-liner installer.

## Documentation

- Replace the readme TODOs: animated GIF showing discovery in action; mermaid architecture diagram showing the collector → correlate → store → api pipeline.
- Threat model / trust posture page: who can connect, what the binary needs, what it doesn't try to defend against.
- A short page on what "activity capture" answers once Phase 1 ships.

## Cross-platform parity

- `gopsutil` claims cross-platform support; verify on Linux (Ubuntu LTS + a recent Fedora), macOS (Apple Silicon + Intel), and Windows (10/11). Document the matrix and any per-platform quirks discovered.
- Build-tagged env reader (`env_darwin.go` / `env_other.go`) needs a Linux-specific implementation that reads `/proc/<pid>/environ` directly — currently `env_other.go` returns nothing.
