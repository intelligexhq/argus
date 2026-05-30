# Intelligex Argus

AI-agent discovery utility. 

Finds AI agents which are running on your mashine and capture their details, including connections, processes and activity. Provides this information through HTTP API 

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
make
# platform specific
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o bin/argus-linux-amd64  ./cmd/argus
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/argus-darwin-arm64 ./cmd/argus
```

## Run

```bash
# http API
make run # runs on custom port 4008
./bin/argus --listen tcp:127.0.0.1:4008

# unix socket
./bin/argus --listen unix:$HOME/.argus/argus.sock --interval 5s
```

Flags: `--db <path>` · `--listen tcp:host:port | unix:/path` · `--interval <dur>`.

## Query the API

Over the default TCP listener:

```bash
# browse rendered docs at http://127.0.0.1:4008/v1/openapi
curl -v -XGET http://127.0.0.1:4008/v1/agents
curl -v -XGET http://127.0.0.1:4008/v1/agents?expand=processes,connections
curl -v -XGET http://127.0.0.1:4008/v1/connections
curl -v -XGET http://127.0.0.1:4008/v1/openapi.yaml

# if started with --listen unix:...
curl --unix-socket ~/.argus/argus.sock http://localhost/v1/agents
```

## Testing

```bash
## we use makefile for local and ci runs

make test       # go test ./...
make race       # go test -race ./...
make fmt-check  # fail if anything is unformatted
make fmt        # gofmt -w .
make vet        # go vet ./...
make ci         # everything CI runs: fmt-check + vet + race + build
```

## Backlog

See [backlog.md](backlog.md) for the roadmap.
