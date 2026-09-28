// Package storage persists sessions and network events: metadata in SQLite,
// bodies in a content-addressed blob store (never inline in the events table).
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)

	"github.com/droidpector/apkinspector/src/model"
)

// ErrNotFound is returned when a session or event does not exist.
var ErrNotFound = errors.New("not found")

// Store is the Network Event Store.
type Store struct {
	db    *sql.DB
	blobs *BlobStore
	dir   string
}

const schemaVersion = 1

var migrations = []string{
	// v1
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		number INTEGER NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		apk TEXT NOT NULL DEFAULT '',
		package TEXT NOT NULL DEFAULT '',
		runtime TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		ended_at INTEGER,
		saved INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		category TEXT NOT NULL,
		state TEXT NOT NULL,
		initiator TEXT NOT NULL DEFAULT 'guest',
		replay_of TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		duration_ms REAL NOT NULL DEFAULT 0,
		package TEXT NOT NULL DEFAULT '',
		protocol TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		scheme TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 0,
		path TEXT NOT NULL DEFAULT '',
		query TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0,
		mime TEXT NOT NULL DEFAULT '',
		req_size INTEGER NOT NULL DEFAULT 0,
		resp_size INTEGER NOT NULL DEFAULT 0,
		encrypted INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		req_body TEXT NOT NULL DEFAULT '',
		resp_body TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS events_session_seq ON events(session_id, seq);
	CREATE TABLE IF NOT EXISTS event_details (
		event_id TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
		data TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS ws_frames (
		event_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		ts INTEGER NOT NULL,
		outgoing INTEGER NOT NULL,
		opcode INTEGER NOT NULL,
		length INTEGER NOT NULL,
		data BLOB,
		truncated INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (event_id, seq)
	);`,
}

// Open opens (creating/migrating) the store in dir: dir/inspector.db and dir/blobs.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory %s: %w", dir, err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "inspector.db")) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	blobs, err := OpenBlobStore(filepath.Join(dir, "blobs"))
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, blobs: blobs, dir: dir}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("database is not usable (is the disk full or the file corrupt?): %w", err)
	}
	var v int
	err := db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='schema_version'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if v > schemaVersion {
		return fmt.Errorf("the database was created by a newer version of droidpector (schema %d > %d)", v, schemaVersion)
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrating database to schema %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	s.blobs.Close()
	return s.db.Close()
}

// Blobs exposes the body store.
func (s *Store) Blobs() *BlobStore { return s.blobs }

// ReadBody implements export.BodySource.
func (s *Store) ReadBody(ref *model.BodyRef) ([]byte, error) {
	if ref == nil || ref.Hash == "" {
		return nil, nil
	}
	return s.blobs.Get(ref.Hash)
}

// ---- sessions ---------------------------------------------------------------

func ms(t time.Time) int64 { return t.UnixMilli() }

// CreateSession inserts a session, assigning the next session number.
func (s *Store) CreateSession(ctx context.Context, sess *model.Session) error {
	if sess.ID == "" {
		sess.ID = model.NewID()
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM sessions`).Scan(&sess.Number); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,number,name,apk,package,runtime,started_at,saved) VALUES(?,?,?,?,?,?,?,?)`,
		sess.ID, sess.Number, sess.Name, sess.APK, sess.Package, sess.Runtime, ms(sess.StartedAt), sess.Saved); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return tx.Commit()
}

// UpdateSession writes the mutable session fields.
func (s *Store) UpdateSession(ctx context.Context, sess *model.Session) error {
	var ended any
	if sess.EndedAt != nil {
		ended = ms(*sess.EndedAt)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET name=?,apk=?,package=?,runtime=?,ended_at=?,saved=? WHERE id=?`,
		sess.Name, sess.APK, sess.Package, sess.Runtime, ended, sess.Saved, sess.ID)
	if err != nil {
		return fmt.Errorf("updating session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const sessionCols = `s.id,s.number,s.name,s.apk,s.package,s.runtime,s.started_at,s.ended_at,s.saved,
	(SELECT COUNT(*) FROM events e WHERE e.session_id=s.id),
	(SELECT COUNT(DISTINCT host) FROM events e WHERE e.session_id=s.id AND e.host<>'' AND e.kind<>'dns'),
	(SELECT COALESCE(SUM(req_size+resp_size),0) FROM events e WHERE e.session_id=s.id)`

func scanSession(row interface{ Scan(...any) error }) (*model.Session, error) {
	var sess model.Session
	var started int64
	var ended sql.NullInt64
	if err := row.Scan(&sess.ID, &sess.Number, &sess.Name, &sess.APK, &sess.Package, &sess.Runtime, &started, &ended, &sess.Saved,
		&sess.Requests, &sess.Domains, &sess.Bytes); err != nil {
		return nil, err
	}
	sess.StartedAt = time.UnixMilli(started)
	if ended.Valid {
		t := time.UnixMilli(ended.Int64)
		sess.EndedAt = &t
	}
	return &sess, nil
}

// Session loads one session with its statistics.
func (s *Store) Session(ctx context.Context, id string) (*model.Session, error) {
	sess, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions s WHERE s.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// Sessions lists sessions, newest first.
func (s *Store) Sessions(ctx context.Context) ([]*model.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions s ORDER BY s.number DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteSession removes a session and its events. Orphaned bodies are removed
// by the next SweepBlobs.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearSession removes all events of a session but keeps the session.
func (s *Store) ClearSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE session_id=?`, id)
	return err
}

// ---- events -----------------------------------------------------------------

// details is the JSON document kept in event_details (everything not needed
// for listing/filtering).
type details struct {
	StatusText      string         `json:"statusText,omitempty"`
	RequestHeaders  model.Headers  `json:"requestHeaders,omitempty"`
	ResponseHeaders model.Headers  `json:"responseHeaders,omitempty"`
	RequestBody     *model.BodyRef `json:"requestBody,omitempty"`
	ResponseBody    *model.BodyRef `json:"responseBody,omitempty"`
	Timing          *model.Timing  `json:"timing,omitempty"`
	TLS             *model.TLSInfo `json:"tls,omitempty"`
	Conn            *model.Conn    `json:"conn,omitempty"`
	DNS             *model.DNSInfo `json:"dns,omitempty"`
}

func bodyHash(b *model.BodyRef) string {
	if b == nil {
		return ""
	}
	return b.Hash
}

const upsertEventSQL = `INSERT INTO events(id,session_id,seq,kind,category,state,initiator,replay_of,started_at,duration_ms,package,protocol,
	method,scheme,host,port,path,query,status,mime,req_size,resp_size,encrypted,error,req_body,resp_body)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET category=excluded.category,state=excluded.state,duration_ms=excluded.duration_ms,
	package=excluded.package,protocol=excluded.protocol,method=excluded.method,scheme=excluded.scheme,host=excluded.host,
	port=excluded.port,path=excluded.path,query=excluded.query,status=excluded.status,mime=excluded.mime,
	req_size=excluded.req_size,resp_size=excluded.resp_size,encrypted=excluded.encrypted,error=excluded.error,
	req_body=excluded.req_body,resp_body=excluded.resp_body`

func upsertEvent(ctx context.Context, tx *sql.Tx, e *model.Event) error {
	initiator := e.Initiator
	if initiator == "" {
		initiator = model.InitiatorGuest
	}
	if _, err := tx.ExecContext(ctx, upsertEventSQL,
		e.ID, e.SessionID, e.Seq, e.Kind, e.Category, e.State, initiator, e.ReplayOf, e.StartedAt.UnixMicro(), e.DurationMs,
		e.Package, e.Protocol, e.Method, e.Scheme, e.Host, e.Port, e.Path, e.Query, e.Status, e.MIME,
		e.RequestSize, e.ResponseSize, e.Encrypted, e.Error, bodyHash(e.RequestBody), bodyHash(e.ResponseBody)); err != nil {
		return fmt.Errorf("storing event %s: %w", e.ID, err)
	}
	d, err := json.Marshal(details{
		StatusText: e.StatusText, RequestHeaders: e.RequestHeaders, ResponseHeaders: e.ResponseHeaders,
		RequestBody: e.RequestBody, ResponseBody: e.ResponseBody, Timing: e.Timing, TLS: e.TLS, Conn: e.Conn, DNS: e.DNS,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO event_details(event_id,data) VALUES(?,?) ON CONFLICT(event_id) DO UPDATE SET data=excluded.data`, e.ID, string(d))
	return err
}

func insertFrame(ctx context.Context, tx *sql.Tx, eventID string, f *model.WSFrame) error {
	// Frames whose event was dropped under backpressure are skipped instead of
	// failing the whole batch on the foreign key.
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO ws_frames(event_id,seq,ts,outgoing,opcode,length,data,truncated)
		SELECT ?,?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM events WHERE id=?)`,
		eventID, f.Seq, f.Time.UnixMicro(), f.Outgoing, f.Opcode, f.Length, f.Data, f.Truncated, eventID)
	return err
}

// PutEvents upserts events synchronously in one transaction (used by the
// async Writer and by tests).
func (s *Store) PutEvents(ctx context.Context, events []*model.Event) error {
	return s.apply(ctx, events, nil)
}

type frameRec struct {
	eventID string
	frame   model.WSFrame
}

func (s *Store) apply(ctx context.Context, events []*model.Event, frames []frameRec) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if err := upsertEvent(ctx, tx, e); err != nil {
			return err
		}
	}
	for i := range frames {
		if err := insertFrame(ctx, tx, frames[i].eventID, &frames[i].frame); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const eventCols = `e.id,e.session_id,e.seq,e.kind,e.category,e.state,e.initiator,e.replay_of,e.started_at,e.duration_ms,e.package,
	e.protocol,e.method,e.scheme,e.host,e.port,e.path,e.query,e.status,e.mime,e.req_size,e.resp_size,e.encrypted,e.error`

func scanSummary(row interface{ Scan(...any) error }) (model.Summary, error) {
	var sm model.Summary
	var started int64
	var sid, rof string
	err := row.Scan(&sm.ID, &sid, &sm.Seq, &sm.Kind, &sm.Category, &sm.State, &sm.Initiator, &rof, &started, &sm.DurationMs,
		&sm.Package, &sm.Protocol, &sm.Method, &sm.Scheme, &sm.Host, &sm.Port, &sm.Path, &sm.Query, &sm.Status, &sm.MIME,
		&sm.RequestSize, &sm.ResponseSize, &sm.Encrypted, &sm.Error)
	sm.StartedAt = time.UnixMicro(started)
	return sm, err
}

// Summaries returns all list rows of a session ordered by sequence.
func (s *Store) Summaries(ctx context.Context, sessionID string) ([]model.Summary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventCols+` FROM events e WHERE e.session_id=? ORDER BY e.seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Summary
	for rows.Next() {
		sm, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// MaxSeq returns the highest event sequence number in a session (0 if none).
func (s *Store) MaxSeq(ctx context.Context, sessionID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE session_id=?`, sessionID).Scan(&n)
	return n, err
}

// Event loads the full event including details and WebSocket frames.
func (s *Store) Event(ctx context.Context, id string) (*model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+eventCols+`, COALESCE(d.data,'{}') FROM events e LEFT JOIN event_details d ON d.event_id=e.id WHERE e.id=?`, id)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Kind == model.KindWebSocket {
		if e.Frames, err = s.Frames(ctx, id, 0, 10000); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func scanEvent(row interface{ Scan(...any) error }) (*model.Event, error) {
	var sm model.Summary
	var started int64
	var sid, rof, data string
	err := row.Scan(&sm.ID, &sid, &sm.Seq, &sm.Kind, &sm.Category, &sm.State, &sm.Initiator, &rof, &started, &sm.DurationMs,
		&sm.Package, &sm.Protocol, &sm.Method, &sm.Scheme, &sm.Host, &sm.Port, &sm.Path, &sm.Query, &sm.Status, &sm.MIME,
		&sm.RequestSize, &sm.ResponseSize, &sm.Encrypted, &sm.Error, &data)
	if err != nil {
		return nil, err
	}
	var d details
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, fmt.Errorf("event %s details are corrupt: %w", sm.ID, err)
	}
	return &model.Event{
		ID: sm.ID, SessionID: sid, Seq: sm.Seq, Kind: sm.Kind, Category: sm.Category, State: sm.State, Initiator: sm.Initiator,
		ReplayOf: rof, StartedAt: time.UnixMicro(started), DurationMs: sm.DurationMs, Package: sm.Package, Protocol: sm.Protocol,
		Method: sm.Method, Scheme: sm.Scheme, Host: sm.Host, Port: sm.Port, Path: sm.Path, Query: sm.Query, Status: sm.Status,
		StatusText: d.StatusText, MIME: sm.MIME, RequestSize: sm.RequestSize, ResponseSize: sm.ResponseSize,
		RequestHeaders: d.RequestHeaders, ResponseHeaders: d.ResponseHeaders, RequestBody: d.RequestBody, ResponseBody: d.ResponseBody,
		Timing: d.Timing, TLS: d.TLS, Conn: d.Conn, DNS: d.DNS, Encrypted: sm.Encrypted, Error: sm.Error,
	}, nil
}

// Frames returns WebSocket frames of an event starting at sequence from.
func (s *Store) Frames(ctx context.Context, eventID string, from int64, limit int) ([]model.WSFrame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,ts,outgoing,opcode,length,data,truncated FROM ws_frames WHERE event_id=? AND seq>=? ORDER BY seq LIMIT ?`, eventID, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.WSFrame
	for rows.Next() {
		var f model.WSFrame
		var ts int64
		if err := rows.Scan(&f.Seq, &ts, &f.Outgoing, &f.Opcode, &f.Length, &f.Data, &f.Truncated); err != nil {
			return nil, err
		}
		f.Time = time.UnixMicro(ts)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Events iterates over the full events of a session in sequence order
// (streaming, for HAR export of large sessions).
func (s *Store) Events(ctx context.Context, sessionID string) iter.Seq2[*model.Event, error] {
	return func(yield func(*model.Event, error) bool) {
		var ids []string
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM events WHERE session_id=? ORDER BY seq`, sessionID)
		if err != nil {
			yield(nil, err)
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				yield(nil, err)
				return
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			e, err := s.Event(ctx, id)
			if errors.Is(err, ErrNotFound) {
				continue // deleted concurrently
			}
			if !yield(e, err) || err != nil {
				return
			}
		}
	}
}

// ReferencedBlobs returns every body hash referenced by any event.
func (s *Store) ReferencedBlobs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT req_body FROM events WHERE req_body<>'' UNION SELECT resp_body FROM events WHERE resp_body<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keep := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		keep[h] = true
	}
	return keep, rows.Err()
}

// SweepBlobs deletes unreferenced bodies older than grace.
func (s *Store) SweepBlobs(ctx context.Context, grace time.Duration) (int, int64, error) {
	keep, err := s.ReferencedBlobs(ctx)
	if err != nil {
		return 0, 0, err
	}
	return s.blobs.Sweep(keep, grace)
}

// RetentionPolicy bounds how much history is kept. Saved sessions and the
// active session are never removed.
type RetentionPolicy struct {
	MaxSessions int           // max unsaved sessions to keep (0 = unlimited)
	MaxAge      time.Duration // remove unsaved sessions older than this (0 = unlimited)
}

// ApplyRetention deletes unsaved sessions beyond the policy and returns their IDs.
func (s *Store) ApplyRetention(ctx context.Context, p RetentionPolicy, activeID string, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at FROM sessions WHERE saved=0 ORDER BY number DESC`)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id      string
		started int64
	}
	var all []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.started); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, c)
	}
	rows.Close()
	var victims []string
	kept := 0
	for _, c := range all {
		if c.id == activeID {
			continue
		}
		tooOld := p.MaxAge > 0 && now.Sub(time.UnixMilli(c.started)) > p.MaxAge
		tooMany := p.MaxSessions > 0 && kept >= p.MaxSessions
		if tooOld || tooMany {
			victims = append(victims, c.id)
			continue
		}
		kept++
	}
	for _, id := range victims {
		if err := s.DeleteSession(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return victims, nil
}

// Integrity runs SQLite's quick_check (used by diagnostics and crash recovery).
func (s *Store) Integrity(ctx context.Context) error {
	var res string
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return err
	}
	if !strings.EqualFold(res, "ok") {
		return fmt.Errorf("database integrity check failed: %s", res)
	}
	return nil
}
