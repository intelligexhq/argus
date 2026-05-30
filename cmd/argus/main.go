// Command argus is the headless agent-discovery collector daemon.
//
// It runs the collectors on an interval, correlates observations into agents,
// persists snapshots to SQLite, and serves the data over a local read API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/intelligexhq/argus/internal/api"
	"github.com/intelligexhq/argus/internal/classify"
	"github.com/intelligexhq/argus/internal/collector"
	"github.com/intelligexhq/argus/internal/correlate"
	"github.com/intelligexhq/argus/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	dbPath := flag.String("db", defaultDBPath(), "path to the SQLite database file")
	listen := flag.String("listen", "tcp:127.0.0.1:8765", "listen address: tcp:host:port or unix:/path")
	interval := flag.Duration("interval", 15*time.Second, "collection interval")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		die("open store", "err", err, "path", *dbPath)
	}
	defer st.Close()

	classifier := classify.NewDefault()
	collectors := []collector.Collector{
		collector.NewProcessCollector(),
		collector.NewNetworkCollector(classifier),
		collector.NewEnvCollector(classifier),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := listenOn(*listen)
	if err != nil {
		die("listen", "addr", *listen, "err", err)
	}
	srv := api.NewServer(st)
	go func() {
		slog.Info("api listening", "addr", *listen)
		if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
			die("serve", "err", err)
		}
	}()

	scan(ctx, collectors, st) // run once at startup so the API has data immediately

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutCtx)
			cancel()
			return
		case <-ticker.C:
			scan(ctx, collectors, st)
		}
	}
}

// die logs an error event and exits with status 1. slog has no Fatal; this is
// the idiomatic replacement for the handful of unrecoverable startup paths.
func die(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func scan(ctx context.Context, collectors []collector.Collector, st *store.Store) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var merged collector.Result
	for _, c := range collectors {
		res, err := c.Collect(cctx)
		if err != nil {
			slog.Error("collector failed", "collector", c.Name(), "err", err)
			continue
		}
		merged.Processes = append(merged.Processes, res.Processes...)
		merged.Connections = append(merged.Connections, res.Connections...)
	}

	now := time.Now()
	agents := correlate.Correlate(now, &merged)
	if err := st.WriteSnapshot(cctx, now, agents, merged.Processes, merged.Connections); err != nil {
		slog.Error("write snapshot", "err", err)
		return
	}
	slog.Info("scan",
		"n_agents", len(agents),
		"n_processes", len(merged.Processes),
		"n_connections", len(merged.Connections),
	)
}

func listenOn(addr string) (net.Listener, error) {
	switch {
	case strings.HasPrefix(addr, "unix:"):
		path := strings.TrimPrefix(addr, "unix:")
		_ = os.Remove(path) // clear a stale socket from a previous run
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		// Restrict to the owning user. Without this the socket inherits the
		// process umask (typically world-readable) and the API is unauthenticated,
		// so any local user could read /v1/agents.
		if err := os.Chmod(path, 0o600); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("chmod socket: %w", err)
		}
		return ln, nil
	case strings.HasPrefix(addr, "tcp:"):
		return net.Listen("tcp", strings.TrimPrefix(addr, "tcp:"))
	default:
		return nil, fmt.Errorf("invalid listen address %q (use unix:/path or tcp:host:port)", addr)
	}
}

func dataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	d := filepath.Join(home, ".argus")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func defaultDBPath() string { return filepath.Join(dataDir(), "argus.db") }
