# Intelligex Argus

AI-agent discovery utility. 

Find all agents which are running on your mashine and capture their details, including connections, processes and activity. 

> Its a simple Go binary with the scope caped at **discovery & activity recording**, no EDR-grade syscall tracing yet. The collectors are unprivileged within current user scope. tool is cross-platform.

## How it works

- TODO: interactive giff. showcase the Intelligex Argus @ work
- TODO: mermaid diagram explaining the architechture.

## Requirements

- Go 1.26+
- SQLite driver - `modernc.org/sqlite`
- Binary builds CGO-free and ships static. `CGO_ENABLED=0`

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
gofmt -l .                          # list unformatted files (empty = clean)
gofmt -w .                          # format the tree
go vet ./...                        # static checks
```

Or via the Makefile (same commands CI runs):

```bash
make test       # go test ./...
make race       # go test -race ./...
make fmt-check  # fail if anything is unformatted
make fmt        # gofmt -w .
make vet        # go vet ./...
make ci         # everything CI runs: fmt-check + vet + race + build
```

## Backlog

Items we are planning to address & explore.

**Lifecycle**
- add linting & validation
- add github actions for testig and building
- how do we distribute? best practices?

**Collection depth**

- Event-collector (eBPF on Linux, EndpointSecurity on macOS, ETW on Windows) to capture short-lived subagents and the full process tree — currently child attachment is one level deep, on purpose.
- Optional privileged mode for system-wide visibility into other users' processes/sockets (today: current-user scope only).

**API & ops**

- Optional token auth on the HTTP API for deployments that bind beyond loopback (default loopback/unix-socket posture stays auth-free).
- Embed Swagger UI assets in the binary instead of loading from CDN, so `/v1/openapi` works fully offline.

**Quality**

- Cross-platform parity: behaviour of the process + network collectors needs verification on Linux, macOS, and Windows (gopsutil claims cross-platform, but we have not exercised the three).
- Replace the readme TODOs (interactive gif demonstrating discovery; mermaid architecture diagram).
