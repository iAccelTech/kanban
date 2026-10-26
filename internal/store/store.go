// Package store owns the SQLite database: a read pool for queries and a single
// writer goroutine for every change. See SCALING.md §4–§6.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("changed by someone else")
)

// InvalidError reports bad input. Its message is safe to show to the user.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &InvalidError{fmt.Sprintf(format, a...)} }

// Actor identifies who is making a change. ClientID is the browser tab, so a tab
// can recognize its own changes when they come back over the live stream.
type Actor struct {
	UserID   int64
	ClientID string
}

// Event is one entry in a board's log.
type Event struct {
	Seq      int64           `json:"seq"`
	BoardID  int64           `json:"board_id"`
	UserID   int64           `json:"user_id"`
	UserName string          `json:"user_name"`
	ClientID string          `json:"client_id,omitempty"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
	Created  int64           `json:"created"`
}

type Store struct {
	read  *sql.DB
	write *sql.DB
	w     *Writer
}

const commonPragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=cache_size(-65536)" + // 64 MiB page cache per connection
	"&_pragma=mmap_size(268435456)" + // 256 MiB memory-mapped reads
	"&_pragma=temp_store(MEMORY)"

// Open opens (creating if needed) the database at path, applies migrations, and
// starts the writer. onCommit receives each committed batch's events, in order,
// on the writer goroutine; it must not block.
func Open(ctx context.Context, path string, onCommit func([]Event)) (*Store, error) {
	write, err := sql.Open("sqlite", "file:"+path+"?"+commonPragmas+
		"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	if err := write.PingContext(ctx); err != nil {
		write.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, write); err != nil {
		write.Close()
		return nil, err
	}

	read, err := sql.Open("sqlite", "file:"+path+"?"+commonPragmas+"&_pragma=query_only(1)")
	if err != nil {
		write.Close()
		return nil, err
	}
	n := runtime.NumCPU()
	read.SetMaxOpenConns(n)
	read.SetMaxIdleConns(n)
	read.SetConnMaxLifetime(0)

	return &Store{read: read, write: write, w: newWriter(write, onCommit)}, nil
}

// Close commits queued writes, then closes the database.
func (s *Store) Close() error {
	s.w.stop()
	s.write.Exec("PRAGMA optimize")
	return errors.Join(s.read.Close(), s.write.Close())
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.read.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func nowMS() int64 { return time.Now().UnixMilli() }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// cleanLine trims s, folds line breaks into spaces, and enforces a length range.
func cleanLine(field, s string, min, max int) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	return checkLen(field, s, min, max)
}

// cleanText trims s but keeps its line breaks.
func cleanText(field, s string, max int) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	return checkLen(field, s, 0, max)
}

func checkLen(field, s string, min, max int) (string, error) {
	n := utf8.RuneCountInString(s)
	if n < min {
		return "", invalid("%s is required", field)
	}
	if n > max {
		return "", invalid("%s is longer than %d characters", field, max)
	}
	return s, nil
}

var (
	BoardColors = []string{"blue", "orange", "green", "red", "purple", "pink", "lime", "sky", "grey"}
	LabelColors = []string{"green", "yellow", "orange", "red", "purple", "blue", "sky", "lime", "pink", "black"}
)

func checkColor(field, c string, allowed []string) error {
	for _, a := range allowed {
		if c == a {
			return nil
		}
	}
	return invalid("%s must be one of %s", field, strings.Join(allowed, ", "))
}

// Opt is a JSON field that distinguishes "absent" from "null".
type Opt[T any] struct {
	Set   bool
	Value *T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}
