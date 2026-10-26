package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrClosed is returned by writes submitted after the store has shut down.
var ErrClosed = errors.New("store closed")

const (
	maxBatch   = 256  // writes per physical transaction
	queueDepth = 1024 // writes waiting for the writer before callers block
)

// Tx is the transaction a write function runs in. It is shared with the other
// writes in the same batch, isolated from them by a savepoint.
type Tx struct {
	*sql.Tx
	actor  Actor
	events []Event
}

// emit records an event in the board's log, bumps the board's version (which
// drives its ETag), and queues the event for live delivery once the batch commits.
func (tx *Tx) emit(boardID int64, typ string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	now := nowMS()
	res, err := tx.Exec(`INSERT INTO events (board_id, user_id, client_id, type, data, created) VALUES (?, ?, ?, ?, ?, ?)`,
		boardID, tx.actor.UserID, tx.actor.ClientID, typ, string(b), now)
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE boards SET version = version + 1 WHERE id = ?`, boardID); err != nil {
		return err
	}
	var name string
	if err := tx.QueryRow(`SELECT name FROM users WHERE id = ?`, tx.actor.UserID).Scan(&name); err != nil {
		return err
	}
	tx.events = append(tx.events, Event{
		Seq: seq, BoardID: boardID, UserID: tx.actor.UserID, UserName: name,
		ClientID: tx.actor.ClientID, Type: typ, Data: b, Created: now,
	})
	return nil
}

type writeReq struct {
	actor Actor
	fn    func(*Tx) error
	done  chan error
}

// Writer funnels every write through one goroutine that owns the only write
// connection, committing whatever is queued as one transaction (group commit).
// See SCALING.md §5.
type Writer struct {
	db       *sql.DB
	reqs     chan writeReq
	onCommit func([]Event)
	quit     chan struct{}
	stopped  chan struct{}
}

func newWriter(db *sql.DB, onCommit func([]Event)) *Writer {
	w := &Writer{
		db:       db,
		reqs:     make(chan writeReq, queueDepth),
		onCommit: onCommit,
		quit:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go w.run()
	return w
}

// Do runs fn inside the writer's next batch and waits for the batch to commit.
// fn must be pure database work: no network calls, no sleeping. If ctx ends
// while waiting, Do returns ctx.Err() but the write may still commit.
func (w *Writer) Do(ctx context.Context, a Actor, fn func(*Tx) error) error {
	r := writeReq{actor: a, fn: fn, done: make(chan error, 1)}
	select {
	case w.reqs <- r:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.quit:
		return ErrClosed
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stop commits whatever is queued and waits for the writer to exit.
func (w *Writer) stop() {
	close(w.quit)
	<-w.stopped
}

func (w *Writer) run() {
	defer close(w.stopped)
	for {
		var batch []writeReq
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
		case <-w.quit:
			w.drain()
			return
		}
	fill:
		for len(batch) < maxBatch {
			select {
			case r := <-w.reqs:
				batch = append(batch, r)
			default:
				break fill
			}
		}
		w.commit(batch)
	}
}

func (w *Writer) drain() {
	var batch []writeReq
	for {
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
			if len(batch) == maxBatch {
				w.commit(batch)
				batch = nil
			}
		default:
			if len(batch) > 0 {
				w.commit(batch)
			}
			return
		}
	}
}

func (w *Writer) commit(batch []writeReq) {
	errs := make([]error, len(batch))
	var events []Event
	err := func() error {
		sqlTx, err := w.db.Begin()
		if err != nil {
			return err
		}
		defer sqlTx.Rollback() // no-op after Commit
		for i, r := range batch {
			// A savepoint per request: one failing write doesn't poison the batch.
			if _, err := sqlTx.Exec("SAVEPOINT req"); err != nil {
				return err
			}
			tx := &Tx{Tx: sqlTx, actor: r.actor}
			if errs[i] = runWrite(r.fn, tx); errs[i] != nil {
				if _, err := sqlTx.Exec("ROLLBACK TO req"); err != nil {
					return err
				}
			} else {
				events = append(events, tx.events...)
			}
			if _, err := sqlTx.Exec("RELEASE req"); err != nil {
				return err
			}
		}
		return sqlTx.Commit()
	}()
	for i, r := range batch {
		if err != nil {
			r.done <- err
		} else {
			r.done <- errs[i]
		}
	}
	if err == nil && len(events) > 0 && w.onCommit != nil {
		w.onCommit(events)
	}
}

func runWrite(fn func(*Tx) error, tx *Tx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic in write: %v", p)
		}
	}()
	return fn(tx)
}
