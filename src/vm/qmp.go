// Package vm is the VM service: runtime profiles, the QEMU adapter (process,
// QMP control channel, serial console), supervision with crash detection and
// recovery, and snapshots.
package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// QMPEvent is an asynchronous QEMU event (SHUTDOWN, RESET, GUEST_PANICKED, …).
type QMPEvent struct {
	Name      string          `json:"event"`
	Data      json.RawMessage `json:"data"`
	Timestamp struct {
		Seconds      int64 `json:"seconds"`
		Microseconds int64 `json:"microseconds"`
	} `json:"timestamp"`
}

// QMPError is an error returned by a QMP command.
type QMPError struct {
	Command string
	Class   string `json:"class"`
	Desc    string `json:"desc"`
}

func (e *QMPError) Error() string {
	return fmt.Sprintf("QEMU rejected %s: %s (%s)", e.Command, e.Desc, e.Class)
}

// ErrQMPClosed is returned after the control connection is lost (QEMU exited).
var ErrQMPClosed = errors.New("the QEMU control channel is closed")

// QMP is a QEMU Machine Protocol client. Commands may be issued concurrently;
// responses are correlated by id.
type QMP struct {
	conn   net.Conn
	w      *json.Encoder
	wmu    sync.Mutex
	events chan QMPEvent

	mu      sync.Mutex
	pending map[string]chan qmpResponse
	nextID  uint64
	closed  bool
	done    chan struct{}
	err     error
}

type qmpResponse struct {
	Return json.RawMessage `json:"return"`
	Error  *QMPError       `json:"error"`
	ID     string          `json:"id"`
	Event  string          `json:"event"`
}

// NewQMP performs the greeting and capabilities negotiation on conn.
func NewQMP(ctx context.Context, conn net.Conn) (*QMP, error) {
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("reading QMP greeting: %w", err)
	}
	var greet struct {
		QMP *struct {
			Version json.RawMessage `json:"version"`
		} `json:"QMP"`
	}
	if json.Unmarshal(line, &greet) != nil || greet.QMP == nil {
		return nil, fmt.Errorf("the QEMU control channel sent an unexpected greeting: %.120q", line)
	}
	q := &QMP{conn: conn, w: json.NewEncoder(conn), events: make(chan QMPEvent, 64), pending: map[string]chan qmpResponse{}, done: make(chan struct{})}
	if err := q.w.Encode(map[string]any{"execute": "qmp_capabilities"}); err != nil {
		return nil, err
	}
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("negotiating QMP capabilities: %w", err)
		}
		var resp qmpResponse
		if json.Unmarshal(line, &resp) != nil || resp.Event != "" {
			continue
		}
		if resp.Error != nil {
			resp.Error.Command = "qmp_capabilities"
			return nil, resp.Error
		}
		break
	}
	conn.SetDeadline(time.Time{})
	go q.readLoop(r)
	return q, nil
}

// Events delivers asynchronous events. The channel is closed when the
// connection ends. Events are dropped if nobody reads them.
func (q *QMP) Events() <-chan QMPEvent { return q.events }

// Done is closed when the connection ends.
func (q *QMP) Done() <-chan struct{} { return q.done }

func (q *QMP) readLoop(r *bufio.Reader) {
	var err error
	defer func() {
		q.mu.Lock()
		q.closed = true
		q.err = err
		for id, ch := range q.pending {
			close(ch)
			delete(q.pending, id)
		}
		q.mu.Unlock()
		close(q.events)
		close(q.done)
	}()
	for {
		var line []byte
		line, err = r.ReadBytes('\n')
		if err != nil {
			return
		}
		var probe struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if probe.Event != "" {
			var ev QMPEvent
			if json.Unmarshal(line, &ev) == nil {
				select {
				case q.events <- ev:
				default:
				}
			}
			continue
		}
		var resp qmpResponse
		if json.Unmarshal(line, &resp) != nil {
			continue
		}
		q.mu.Lock()
		ch := q.pending[resp.ID]
		delete(q.pending, resp.ID)
		q.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

// Execute runs a command and decodes its "return" value into out (may be nil).
func (q *QMP) Execute(ctx context.Context, command string, args any, out any) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQMPClosed
	}
	q.nextID++
	id := fmt.Sprintf("c%d", q.nextID)
	ch := make(chan qmpResponse, 1)
	q.pending[id] = ch
	q.mu.Unlock()

	msg := map[string]any{"execute": command, "id": id}
	if args != nil {
		msg["arguments"] = args
	}
	q.wmu.Lock()
	err := q.w.Encode(msg)
	q.wmu.Unlock()
	if err != nil {
		q.forget(id)
		return fmt.Errorf("sending %s to QEMU: %w", command, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return ErrQMPClosed
		}
		if resp.Error != nil {
			resp.Error.Command = command
			return resp.Error
		}
		if out != nil && len(resp.Return) > 0 {
			return json.Unmarshal(resp.Return, out)
		}
		return nil
	case <-ctx.Done():
		q.forget(id)
		return fmt.Errorf("QEMU did not answer %s: %w", command, ctx.Err())
	}
}

func (q *QMP) forget(id string) {
	q.mu.Lock()
	delete(q.pending, id)
	q.mu.Unlock()
}

// HMP runs a human monitor command (used for snapshot listing).
func (q *QMP) HMP(ctx context.Context, cmd string) (string, error) {
	var out string
	err := q.Execute(ctx, "human-monitor-command", map[string]any{"command-line": cmd}, &out)
	return out, err
}

// Close closes the connection.
func (q *QMP) Close() error { return q.conn.Close() }

// Status returns the run state ("running", "paused", "shutdown", …).
func (q *QMP) Status(ctx context.Context) (string, error) {
	var st struct {
		Status string `json:"status"`
	}
	err := q.Execute(ctx, "query-status", nil, &st)
	return st.Status, err
}

// ---- snapshots ---------------------------------------------------------------

// Snapshot describes one internal VM snapshot.
type Snapshot struct {
	Tag     string    `json:"tag"`
	Created time.Time `json:"created"`
	VMSize  string    `json:"vmSize"`
}

// runJob runs a snapshot-save/load/delete job and waits for it.
func (q *QMP) runJob(ctx context.Context, command string, args map[string]any) error {
	jobID := fmt.Sprintf("%s-%d", command, time.Now().UnixNano())
	args["job-id"] = jobID
	if err := q.Execute(ctx, command, args, nil); err != nil {
		return err
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		var jobs []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := q.Execute(ctx, "query-jobs", nil, &jobs); err != nil {
			return err
		}
		found := false
		for _, j := range jobs {
			if j.ID != jobID {
				continue
			}
			found = true
			if j.Status == "concluded" || j.Status == "null" {
				q.Execute(ctx, "job-dismiss", map[string]any{"id": jobID}, nil)
				if j.Error != "" {
					return fmt.Errorf("%s failed: %s", command, j.Error)
				}
				return nil
			}
		}
		if !found {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// SaveSnapshot saves the complete VM state (RAM + devices + disk) under tag.
func (q *QMP) SaveSnapshot(ctx context.Context, tag, node string) error {
	return q.runJob(ctx, "snapshot-save", map[string]any{"tag": tag, "vmstate": node, "devices": []string{node}})
}

// LoadSnapshot restores tag.
func (q *QMP) LoadSnapshot(ctx context.Context, tag, node string) error {
	return q.runJob(ctx, "snapshot-load", map[string]any{"tag": tag, "vmstate": node, "devices": []string{node}})
}

// DeleteSnapshot removes tag.
func (q *QMP) DeleteSnapshot(ctx context.Context, tag, node string) error {
	return q.runJob(ctx, "snapshot-delete", map[string]any{"tag": tag, "devices": []string{node}})
}

// ListSnapshots parses HMP "info snapshots".
func (q *QMP) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := q.HMP(ctx, "info snapshots")
	if err != nil {
		return nil, err
	}
	return parseSnapshotList(out), nil
}

// parseSnapshotList parses:
//
//	List of snapshots present on all disks:
//	ID        TAG               VM SIZE                DATE     VM CLOCK     ICOUNT
//	--        boot              512 MiB 2026-09-28 13:01:02  00:05:12.123
func parseSnapshotList(out string) []Snapshot {
	var snaps []Snapshot
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 6 || f[0] == "ID" || strings.HasPrefix(l, "List") || strings.HasPrefix(l, "There is no") {
			continue
		}
		// ID TAG... SIZE UNIT DATE TIME CLOCK [ICOUNT]; tags may contain spaces,
		// so anchor on the date.
		for i := 4; i+1 < len(f); i++ {
			t, err := time.ParseInLocation("2006-01-02 15:04:05", f[i]+" "+f[i+1], time.Local)
			if err != nil {
				continue
			}
			snaps = append(snaps, Snapshot{Tag: strings.Join(f[1:i-2], " "), VMSize: f[i-2] + " " + f[i-1], Created: t})
			break
		}
	}
	return snaps
}
