package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iAccelTech/kanban/internal/hub"
	"github.com/iAccelTech/kanban/internal/store"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := hub.New()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"), h.Publish)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, h, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(func() {
		h.Close()
		srv.Close()
		st.Close()
	})
	return srv
}

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path string, body any, out any, headers ...string) *http.Response {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, r)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if out != nil && res.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return res
}

func (c client) ok(method, path string, body any, out any) {
	c.t.Helper()
	if res := c.do(method, path, body, out); res.StatusCode >= 300 {
		c.t.Fatalf("%s %s: status %d", method, path, res.StatusCode)
	}
}

func TestBoardFlowAndETag(t *testing.T) {
	c := client{t, newServer(t)}
	var b store.Board
	if res := c.do("POST", "/api/boards", map[string]string{"title": "Plan"}, &b); res.StatusCode != 201 {
		t.Fatalf("create board: %d", res.StatusCode)
	}
	var l store.List
	c.ok("POST", "/api/boards/"+itoa(b.ID)+"/lists", map[string]string{"title": "Todo"}, &l)
	var card1, card2 store.Card
	c.ok("POST", "/api/lists/"+itoa(l.ID)+"/cards", map[string]string{"title": "one"}, &card1)
	c.ok("POST", "/api/lists/"+itoa(l.ID)+"/cards", map[string]string{"title": "two"}, &card2)
	c.ok("POST", "/api/cards/"+itoa(card2.ID)+"/move", map[string]any{"list_id": l.ID, "after_id": card1.ID}, nil)

	var snap store.Snapshot
	res := c.do("GET", "/api/boards/"+itoa(b.ID), nil, &snap)
	if len(snap.Cards) != 2 || snap.Cards[0].Title != "two" {
		t.Fatalf("snapshot cards = %+v", snap.Cards)
	}
	etag := res.Header.Get("ETag")
	if res := c.do("GET", "/api/boards/"+itoa(b.ID), nil, nil, "If-None-Match", etag); res.StatusCode != 304 {
		t.Fatalf("matching ETag: %d", res.StatusCode)
	}
	c.ok("PATCH", "/api/cards/"+itoa(card1.ID), map[string]string{"title": "one!"}, nil)
	if res := c.do("GET", "/api/boards/"+itoa(b.ID), nil, nil, "If-None-Match", etag); res.StatusCode != 200 {
		t.Fatalf("stale ETag: %d", res.StatusCode)
	}
}

func TestErrors(t *testing.T) {
	c := client{t, newServer(t)}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{"POST", "/api/boards", map[string]string{"title": ""}, 400},
		{"POST", "/api/boards", map[string]string{"title": "x", "color": "chartreuse"}, 400},
		{"POST", "/api/boards", map[string]string{"title": "x", "bogus": "1"}, 400},
		{"GET", "/api/boards/999", nil, 404},
		{"GET", "/api/boards/abc", nil, 404},
		{"GET", "/api/nope", nil, 404},
		{"PATCH", "/api/cards/1", map[string]any{"title": "x", "version": 99}, 404},
	}
	for _, tc := range cases {
		if res := c.do(tc.method, tc.path, tc.body, nil); res.StatusCode != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, res.StatusCode, tc.want)
		}
	}
}

func TestConflict(t *testing.T) {
	c := client{t, newServer(t)}
	var b store.Board
	var l store.List
	var card store.Card
	c.ok("POST", "/api/boards", map[string]string{"title": "B"}, &b)
	c.ok("POST", "/api/boards/"+itoa(b.ID)+"/lists", map[string]string{"title": "L"}, &l)
	c.ok("POST", "/api/lists/"+itoa(l.ID)+"/cards", map[string]string{"title": "c"}, &card)
	c.ok("PATCH", "/api/cards/"+itoa(card.ID), map[string]any{"description": "a", "version": card.Version}, nil)
	if res := c.do("PATCH", "/api/cards/"+itoa(card.ID), map[string]any{"description": "b", "version": card.Version}, nil); res.StatusCode != 409 {
		t.Fatalf("stale write: %d", res.StatusCode)
	}
}

// TestEventStream checks replay from ?after=, live delivery, and Last-Event-ID.
func TestEventStream(t *testing.T) {
	srv := newServer(t)
	c := client{t, srv}
	var b store.Board
	var l store.List
	c.ok("POST", "/api/boards", map[string]string{"title": "B"}, &b)
	c.ok("POST", "/api/boards/"+itoa(b.ID)+"/lists", map[string]string{"title": "L"}, &l)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/boards/"+itoa(b.ID)+"/events?after=0", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	events := make(chan store.Event, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev store.Event
				if json.Unmarshal([]byte(data), &ev) == nil && ev.Type != "" {
					events <- ev
				}
			}
		}
		close(events)
	}()
	next := func() store.Event {
		select {
		case ev := <-events:
			return ev
		case <-ctx.Done():
			t.Fatal("timed out waiting for event")
		}
		return store.Event{}
	}
	if ev := next(); ev.Type != "board.created" {
		t.Fatalf("replay 1: %s", ev.Type)
	}
	if ev := next(); ev.Type != "list.created" {
		t.Fatalf("replay 2: %s", ev.Type)
	}
	c.ok("POST", "/api/lists/"+itoa(l.ID)+"/cards", map[string]string{"title": "live"}, nil)
	ev := next()
	if ev.Type != "card.created" || ev.UserName != "Dev User" {
		t.Fatalf("live: %+v", ev)
	}

	// Reconnect with Last-Event-ID: only newer events come back.
	cancel()
	c.ok("POST", "/api/lists/"+itoa(l.ID)+"/cards", map[string]string{"title": "missed"}, nil)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	req, _ = http.NewRequestWithContext(ctx2, "GET", srv.URL+"/api/boards/"+itoa(b.ID)+"/events", nil)
	req.Header.Set("Last-Event-ID", itoa(ev.Seq))
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	sc := bufio.NewScanner(res2.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var got store.Event
			json.Unmarshal([]byte(data), &got)
			if got.Type != "card.created" || !strings.Contains(string(got.Data), "missed") {
				t.Fatalf("after reconnect: %s %s", got.Type, got.Data)
			}
			return
		}
	}
	t.Fatal("no event after reconnect")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
