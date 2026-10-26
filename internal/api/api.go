// Package api is the JSON HTTP API under /api/.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iAccelTech/kanban/internal/hub"
	"github.com/iAccelTech/kanban/internal/store"
)

// devUserID is the built-in user every request acts as until login exists.
// actor() is the one place that changes when auth lands.
const devUserID = 1

const requestTimeout = 10 * time.Second

type API struct {
	store *store.Store
	hub   *hub.Hub
	log   *slog.Logger
}

func New(s *store.Store, h *hub.Hub, log *slog.Logger) *API {
	return &API{store: s, hub: h, log: log}
}

// Handler returns the API routes wrapped in logging, panic recovery, and
// per-request timeouts.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	a.routes(mux)
	return a.middleware(mux)
}

func (a *API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /api/me", a.json(a.me))

	mux.HandleFunc("GET /api/boards", a.json(a.listBoards))
	mux.HandleFunc("POST /api/boards", a.json(a.createBoard))
	mux.HandleFunc("GET /api/boards/{id}", a.getBoard)
	mux.HandleFunc("PATCH /api/boards/{id}", a.json(a.updateBoard))
	mux.HandleFunc("PUT /api/boards/{id}/star", a.json(a.star(true)))
	mux.HandleFunc("DELETE /api/boards/{id}/star", a.json(a.star(false)))
	mux.HandleFunc("GET /api/boards/{id}/activity", a.json(a.activity))
	mux.HandleFunc("GET /api/boards/{id}/events", a.events)
	mux.HandleFunc("POST /api/boards/{id}/lists", a.json(a.createList))
	mux.HandleFunc("POST /api/boards/{id}/labels", a.json(a.createLabel))

	mux.HandleFunc("PATCH /api/lists/{id}", a.json(a.updateList))
	mux.HandleFunc("POST /api/lists/{id}/move", a.json(a.moveList))
	mux.HandleFunc("POST /api/lists/{id}/cards", a.json(a.createCard))

	mux.HandleFunc("GET /api/cards/{id}", a.json(a.getCard))
	mux.HandleFunc("PATCH /api/cards/{id}", a.json(a.updateCard))
	mux.HandleFunc("POST /api/cards/{id}/move", a.json(a.moveCard))
	mux.HandleFunc("PUT /api/cards/{id}/labels/{label}", a.json(a.cardLabel(true)))
	mux.HandleFunc("DELETE /api/cards/{id}/labels/{label}", a.json(a.cardLabel(false)))
	mux.HandleFunc("POST /api/cards/{id}/checklists", a.json(a.createChecklist))
	mux.HandleFunc("POST /api/cards/{id}/comments", a.json(a.createComment))

	mux.HandleFunc("PATCH /api/labels/{id}", a.json(a.updateLabel))
	mux.HandleFunc("DELETE /api/labels/{id}", a.json(a.deleteLabel))

	mux.HandleFunc("PATCH /api/checklists/{id}", a.json(a.renameChecklist))
	mux.HandleFunc("DELETE /api/checklists/{id}", a.json(a.deleteChecklist))
	mux.HandleFunc("POST /api/checklists/{id}/items", a.json(a.createCheckItem))
	mux.HandleFunc("PATCH /api/items/{id}", a.json(a.updateCheckItem))
	mux.HandleFunc("DELETE /api/items/{id}", a.json(a.deleteCheckItem))

	mux.HandleFunc("PATCH /api/comments/{id}", a.json(a.updateComment))
	mux.HandleFunc("DELETE /api/comments/{id}", a.json(a.deleteComment))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
}

// ---- plumbing ----

type created struct{ v any }

// json adapts a handler that returns a value to one that writes it as JSON,
// mapping store errors to HTTP statuses.
func (a *API) json(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		status := http.StatusOK
		if c, ok := v.(created); ok {
			status, v = http.StatusCreated, c.v
		}
		writeJSON(w, status, v)
	}
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *store.InvalidError
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "invalid", inv.Msg)
	case errors.As(err, &syn), errors.As(err, &typ), errors.Is(err, errBadJSON):
		writeError(w, http.StatusBadRequest, "invalid", "malformed JSON: "+err.Error())
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "this was changed by someone else; reload and try again")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, store.ErrClosed):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "server busy, try again")
	case errors.Is(err, context.Canceled):
		// The client went away; nobody is listening.
	default:
		a.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		v = map[string]bool{"ok": true}
	}
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

var errBadJSON = errors.New("unexpected data after JSON object")

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &store.InvalidError{Msg: err.Error()}
		}
		return err
	}
	if dec.More() {
		return errBadJSON
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, store.ErrNotFound
	}
	return id, nil
}

func actor(r *http.Request) store.Actor {
	client := r.Header.Get("X-Client-ID")
	if len(client) > 64 {
		client = client[:64]
	}
	return store.Actor{UserID: devUserID, ClientID: client}
}

// ---- middleware ----

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and SetWriteDeadline.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *API) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		sw.Header().Set("X-Content-Type-Options", "nosniff")
		sw.Header().Set("Cache-Control", "no-store")
		defer func() {
			if p := recover(); p != nil {
				a.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(p))
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			a.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds())
		}()
		if !strings.HasSuffix(r.URL.Path, "/events") {
			ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(sw, r)
	})
}

// ---- handlers ----

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subscribers": a.hub.Count()})
}

func (a *API) me(r *http.Request) (any, error) {
	return map[string]any{"id": devUserID, "name": "Dev User"}, nil
}

func (a *API) listBoards(r *http.Request) (any, error) {
	return a.store.Boards(r.Context(), devUserID)
}

func (a *API) createBoard(r *http.Request) (any, error) {
	var in struct{ Title, Color string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	b, err := a.store.CreateBoard(r.Context(), actor(r), in.Title, in.Color)
	return created{b}, err
}

// getBoard serves the board snapshot with an ETag tied to the board's version,
// so an up-to-date client gets a 304 without the snapshot being read.
func (a *API) getBoard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	etag := func(v int64) string { return fmt.Sprintf(`"b%d-v%d"`, id, v) }
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		v, err := a.store.BoardVersion(r.Context(), id)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if inm == etag(v) {
			w.Header().Set("ETag", inm)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	snap, err := a.store.Board(r.Context(), devUserID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(snap.Board.Version))
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, snap)
}

func (a *API) updateBoard(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var p store.BoardPatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	return a.store.UpdateBoard(r.Context(), actor(r), id, p)
}

func (a *API) star(on bool) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		return nil, a.store.SetStar(r.Context(), actor(r), id, on)
	}
}

func (a *API) activity(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	if _, err := a.store.BoardVersion(r.Context(), id); err != nil {
		return nil, err
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return a.store.EventsBefore(r.Context(), id, before, limit)
}

func (a *API) createList(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Title string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	l, err := a.store.CreateList(r.Context(), actor(r), id, in.Title)
	return created{l}, err
}

func (a *API) updateList(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var p store.ListPatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	return a.store.UpdateList(r.Context(), actor(r), id, p)
}

// moveInput names the new neighbors: BeforeID is the item that will sit
// directly above (or left of) the moved one, AfterID the one directly below.
type moveInput struct {
	ListID   int64 `json:"list_id"`
	BeforeID int64 `json:"before_id"`
	AfterID  int64 `json:"after_id"`
}

func (a *API) moveList(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in moveInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return a.store.MoveList(r.Context(), actor(r), id, in.BeforeID, in.AfterID)
}

func (a *API) createCard(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Title string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	c, err := a.store.CreateCard(r.Context(), actor(r), id, in.Title)
	return created{c}, err
}

func (a *API) getCard(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return a.store.CardDetail(r.Context(), id)
}

func (a *API) updateCard(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var p store.CardPatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	return a.store.UpdateCard(r.Context(), actor(r), id, p)
}

func (a *API) moveCard(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in moveInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return a.store.MoveCard(r.Context(), actor(r), id, in.ListID, in.BeforeID, in.AfterID)
}

func (a *API) cardLabel(on bool) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		label, err := pathID(r, "label")
		if err != nil {
			return nil, err
		}
		return a.store.SetCardLabel(r.Context(), actor(r), id, label, on)
	}
}

func (a *API) createLabel(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Name, Color string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	l, err := a.store.CreateLabel(r.Context(), actor(r), id, in.Name, in.Color)
	return created{l}, err
}

func (a *API) updateLabel(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var p store.LabelPatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	return a.store.UpdateLabel(r.Context(), actor(r), id, p)
}

func (a *API) deleteLabel(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return nil, a.store.DeleteLabel(r.Context(), actor(r), id)
}

func (a *API) createChecklist(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Title string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	d, err := a.store.CreateChecklist(r.Context(), actor(r), id, in.Title)
	return created{d}, err
}

func (a *API) renameChecklist(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Title string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return a.store.RenameChecklist(r.Context(), actor(r), id, in.Title)
}

func (a *API) deleteChecklist(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return a.store.DeleteChecklist(r.Context(), actor(r), id)
}

func (a *API) createCheckItem(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Text string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	d, err := a.store.CreateCheckItem(r.Context(), actor(r), id, in.Text)
	return created{d}, err
}

func (a *API) updateCheckItem(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var p store.CheckItemPatch
	if err := decode(r, &p); err != nil {
		return nil, err
	}
	return a.store.UpdateCheckItem(r.Context(), actor(r), id, p)
}

func (a *API) deleteCheckItem(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return a.store.DeleteCheckItem(r.Context(), actor(r), id)
}

func (a *API) createComment(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Body string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	d, err := a.store.CreateComment(r.Context(), actor(r), id, in.Body)
	return created{d}, err
}

func (a *API) updateComment(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var in struct{ Body string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return a.store.UpdateComment(r.Context(), actor(r), id, in.Body)
}

func (a *API) deleteComment(r *http.Request) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return a.store.DeleteComment(r.Context(), actor(r), id)
}
