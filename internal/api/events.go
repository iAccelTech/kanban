package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	heartbeat   = 20 * time.Second
	replayPage  = 500
	replayLimit = 5000 // further behind than this, the client reloads the snapshot
)

// events streams a board's changes as server-sent events. The client passes the
// snapshot's seq as ?after=; on reconnect the browser sends Last-Event-ID, and
// everything it missed is replayed from the event log first.
func (a *API) events(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := a.store.BoardVersion(ctx, id); err != nil {
		a.fail(w, r, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}

	// Subscribe before replaying, so nothing committed in between is lost; the
	// seq check below drops anything the replay already sent.
	sub := a.hub.Subscribe(id)
	defer a.hub.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // the server's WriteTimeout doesn't apply to streams
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell nginx not to buffer
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")

	sent := 0
	for {
		evs, err := a.store.EventsAfter(ctx, id, after, replayPage)
		if err != nil {
			return
		}
		for _, ev := range evs {
			if err := writeEvent(w, ev.Seq, ev); err != nil {
				return
			}
			after = ev.Seq
		}
		sent += len(evs)
		if sent > replayLimit {
			fmt.Fprint(w, "event: reset\ndata: {}\n\n")
			rc.Flush()
			return
		}
		if len(evs) < replayPage {
			break
		}
	}
	if rc.Flush() != nil {
		return
	}

	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case ev := <-sub.C:
			if ev.Seq <= after {
				continue
			}
			if writeEvent(w, ev.Seq, ev) != nil || rc.Flush() != nil {
				return
			}
			after = ev.Seq
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-sub.Kicked():
			return
		case <-ctx.Done():
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, seq int64, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, b)
	return err
}
