// Package store persists scan results to a local SQLite database (modernc driver, no CGO, binary cross-compiles and ships static).
//
// Concurrency model: the engine is the single writer (one goroutine, one scan
// at a time); the API only reads. WAL mode lets reads run concurrently with the
// writer. Timestamps are stored as Unix millis (INTEGER) to avoid driver
// datetime quirks.
package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/intelligexhq/argus/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate creates the schema on a fresh database. There is no in-place
// migration path while the project maturing: schema changes during this
// phase are made by editing this CREATE block, and required removing old sqlite file.
// next: when external users exist, this approach inverts and additive ALTERs
// or a versioned migration runner go here.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS agents (
  id         TEXT PRIMARY KEY,
  type       TEXT NOT NULL,
  name       TEXT NOT NULL,
  confidence REAL NOT NULL,
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS processes (
  pid        INTEGER NOT NULL,
  ppid       INTEGER NOT NULL,
  name       TEXT,
  exe        TEXT,
  cmdline    TEXT,
  started_at INTEGER,
  agent_id   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS connections (
  pid            INTEGER NOT NULL,
  remote_ip      TEXT,
  remote_host    TEXT NOT NULL DEFAULT '',
  remote_port    INTEGER,
  endpoint       TEXT,
  classification TEXT,
  observed_at    INTEGER,
  agent_id       TEXT NOT NULL DEFAULT '',
  source         TEXT NOT NULL DEFAULT 'socket',
  source_detail  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS scan_runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at    INTEGER NOT NULL,
  n_agents      INTEGER NOT NULL,
  n_processes   INTEGER NOT NULL,
  n_connections INTEGER NOT NULL
);`)
	return err
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v) }

// WriteSnapshot replaces the current process/connection snapshot and upserts the
// agent inventory (preserving each agent's original first_seen).
func (s *Store) WriteSnapshot(ctx context.Context, now time.Time, agents []model.Agent, procs []model.Process, conns []model.Connection) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, a := range agents {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agents(id,type,name,confidence,first_seen,last_seen)
			 VALUES(?,?,?,?,?,?)
			 ON CONFLICT(id) DO UPDATE SET
			   last_seen=excluded.last_seen,
			   confidence=excluded.confidence,
			   name=excluded.name`,
			a.ID, a.Type, a.Name, a.Confidence, ms(a.FirstSeen), ms(a.LastSeen),
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM processes`); err != nil {
		return err
	}
	for _, p := range procs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO processes(pid,ppid,name,exe,cmdline,started_at,agent_id)
			 VALUES(?,?,?,?,?,?,?)`,
			p.PID, p.PPID, p.Name, p.Exe, p.Cmdline, ms(p.StartedAt), p.AgentID,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM connections`); err != nil {
		return err
	}
	for _, c := range conns {
		source := c.Source
		if source == "" {
			source = "socket"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO connections(pid,remote_ip,remote_host,remote_port,endpoint,classification,observed_at,agent_id,source,source_detail)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			c.PID, c.RemoteIP, c.RemoteHost, c.RemotePort, c.Endpoint, c.Classification, ms(c.ObservedAt), c.AgentID, source, c.SourceDetail,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO scan_runs(started_at,n_agents,n_processes,n_connections) VALUES(?,?,?,?)`,
		ms(now), len(agents), len(procs), len(conns),
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ListAgents(ctx context.Context) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,type,name,confidence,first_seen,last_seen FROM agents ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []model.Agent
	byID := map[string]*model.Agent{}
	for rows.Next() {
		var a model.Agent
		var fs, ls int64
		if err := rows.Scan(&a.ID, &a.Type, &a.Name, &a.Confidence, &fs, &ls); err != nil {
			return nil, err
		}
		a.FirstSeen, a.LastSeen = fromMS(fs), fromMS(ls)
		a.PIDs = []int32{}
		agents = append(agents, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}

	pidRows, err := s.db.QueryContext(ctx, `SELECT agent_id,pid FROM processes WHERE agent_id<>''`)
	if err != nil {
		return nil, err
	}
	defer pidRows.Close()
	for pidRows.Next() {
		var id string
		var pid int32
		if err := pidRows.Scan(&id, &pid); err != nil {
			return nil, err
		}
		if a, ok := byID[id]; ok {
			a.PIDs = append(a.PIDs, pid)
		}
	}
	return agents, pidRows.Err()
}

func (s *Store) ListProcesses(ctx context.Context) ([]model.Process, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pid,ppid,name,exe,cmdline,started_at,agent_id FROM processes ORDER BY pid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Process
	for rows.Next() {
		var p model.Process
		var st int64
		if err := rows.Scan(&p.PID, &p.PPID, &p.Name, &p.Exe, &p.Cmdline, &st, &p.AgentID); err != nil {
			return nil, err
		}
		p.StartedAt = fromMS(st)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListConnections(ctx context.Context) ([]model.Connection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pid,remote_ip,remote_host,remote_port,endpoint,classification,observed_at,agent_id,source,source_detail
		 FROM connections ORDER BY pid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Connection
	for rows.Next() {
		var c model.Connection
		var ob int64
		if err := rows.Scan(&c.PID, &c.RemoteIP, &c.RemoteHost, &c.RemotePort, &c.Endpoint, &c.Classification, &ob, &c.AgentID, &c.Source, &c.SourceDetail); err != nil {
			return nil, err
		}
		c.ObservedAt = fromMS(ob)
		out = append(out, c)
	}
	return out, rows.Err()
}
