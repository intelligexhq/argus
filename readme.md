# Intelligex Argus

Headless AI-agent discovery tool. 

Simple Go binary that aims to find all agents running on a machine (Claude Code, Cursor, Copilot, Ollama, custom LLM clients, etc.). It identifies the agents, env variables they use, endpoints they talk to, and the child processes they spawn. presents all information over a local API endpoint.

> Scope is **discovery & activity recording**, no EDR-grade syscall tracing yet. The collector is unprivileged / current user scope. tool is cross-platform.

## How it works

- TODO: interactive giff. showcase the Intelligex Argus @ work
- TODO: mermaid diagram explaining the architechture.

## Requirements

- Go 1.22+ (built with 1.26). The SQLite driver (`modernc.org/sqlite`) which is pure Go.
the binary builds CGO-free and ships static. (we use `CGO_ENABLED=0` to drop it.)

## Build

```bash
# general
go build -o bin/argus ./cmd/argus
# platform specific
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o bin/argus-linux-amd64  ./cmd/argus
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/argus-darwin-arm64 ./cmd/argus
```

## Run

```bash
./bin/argus
# default: TCP listener at 127.0.0.1:8765
./bin/argus --listen tcp:127.0.0.1:4000
./bin/argus --listen unix:$HOME/.argus/argus.sock --interval 5s
```

Flags: `--db <path>` · `--listen tcp:host:port | unix:/path` · `--interval <dur>`.

## Query the API

Over the default TCP listener:

```bash
curl -v -XGET http://127.0.0.1:8765/v1/agents
curl -v -XGET http://127.0.0.1:8765/v1/agents?expand=processes,connections
curl -v -XGET http://127.0.0.1:8765/v1/connections
curl -v -XGET http://127.0.0.1:8765/v1/openapi.yaml
# browse rendered docs at http://127.0.0.1:8765/v1/openapi

# if started with --listen unix:...
curl --unix-socket ~/.argus/argus.sock http://localhost/v1/agents
```

API endpoints:

- `GET /healthz`
- `GET /v1/agents`
- `GET /v1/processes`
- `GET /v1/connections`
- `GET /v1/openapi` (Swagger UI, HTML)
- `GET /v1/openapi.yaml` (raw spec)

## Testing

```bash
go test ./...                       # run all unit tests
go test -v ./internal/correlate/... # one package, verbose
go test -race ./...                 # with the race detector
```


## Layout

```
cmd/argus           daemon entry point (flags, scan loop, signals)
internal/model      canonical entities (Agent, Process, Connection)
internal/collector  Collector interface + process, network, and env collectors
internal/classify   endpoint classification (locality + model-service labels)
internal/correlate  agent identification + child-process attachment
internal/store      SQLite persistence (pure-Go modernc, WAL)
internal/api        local HTTP/JSON read API + embedded OpenAPI spec
```

## Backlog

Items to address / explore. The scaffold is intentionally narrow — these expand its reach without compromising the unprivileged-by-default posture.

**Lifecycle**
- add linting & validation
- add github actions for testig and building
- how do we distribute? best practices?

**Collection depth**

- DNS/SNI capture collector to replace best-effort reverse-DNS labelling of remote IPs (current heuristic is brittle on shared CDNs and private model hosts). The unprivileged env-var collector closes the gap for agents that announce their endpoint via configuration (`OLLAMA_HOST`, `OPENAI_BASE_URL`, `AZURE_OPENAI_ENDPOINT`, …); DNS/SNI is still needed for the hardcoded-default case (Claude Code → `api.anthropic.com`, etc.).
- Wire `classify.RegisterModelHost` to a config file so private/self-hosted model endpoints can be declared declaratively, not in code.
- Event-collector (eBPF on Linux, EndpointSecurity on macOS, ETW on Windows) to capture short-lived subagents and the full process tree — currently child attachment is one level deep, on purpose.
- Optional privileged mode for system-wide visibility into other users' processes/sockets (today: current-user scope only).

**API & ops**

- Optional token auth on the HTTP API for deployments that bind beyond loopback (default loopback/unix-socket posture stays auth-free).
- Embed Swagger UI assets in the binary instead of loading from CDN, so `/v1/openapi` works fully offline.

**Quality**

- Test coverage: `classify`, `correlate`, `api`, `collector`, and `store` have unit tests; `cmd/argus` (main wiring) and `internal/model` (pure types) are intentionally untested.
- Cross-platform parity: behaviour of the process + network collectors needs verification on Linux, macOS, and Windows (gopsutil claims cross-platform, but we have not exercised the three).
- Replace the readme TODOs (interactive gif demonstrating discovery; mermaid architecture diagram).
