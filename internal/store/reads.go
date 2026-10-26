package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type Board struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Color      string `json:"color"`
	Starred    bool   `json:"starred"`
	Created    int64  `json:"created"`
	ArchivedAt *int64 `json:"archived_at"`
	Version    int64  `json:"version"`
}

type List struct {
	ID         int64  `json:"id"`
	BoardID    int64  `json:"board_id"`
	Title      string `json:"title"`
	Rank       string `json:"rank"`
	ArchivedAt *int64 `json:"archived_at"`
	Version    int64  `json:"version"`
}

// Card is what a board shows for a card: enough for the front of the card.
type Card struct {
	ID             int64   `json:"id"`
	BoardID        int64   `json:"board_id"`
	ListID         int64   `json:"list_id"`
	Title          string  `json:"title"`
	Rank           string  `json:"rank"`
	Due            *int64  `json:"due"`
	DueDone        bool    `json:"due_done"`
	HasDescription bool    `json:"has_description"`
	LabelIDs       []int64 `json:"label_ids"`
	CheckTotal     int     `json:"check_total"`
	CheckDone      int     `json:"check_done"`
	Comments       int     `json:"comments"`
	Updated        int64   `json:"updated"`
	ArchivedAt     *int64  `json:"archived_at"`
	Version        int64   `json:"version"`
}

// CardDetail is everything the card modal shows.
type CardDetail struct {
	Card
	Description     string      `json:"description"`
	DescriptionHTML string      `json:"description_html"`
	Created         int64       `json:"created"`
	Checklists      []Checklist `json:"checklists"`
	CommentList     []Comment   `json:"comment_list"`
}

type Label struct {
	ID      int64  `json:"id"`
	BoardID int64  `json:"board_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
}

type Member struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type Checklist struct {
	ID     int64       `json:"id"`
	CardID int64       `json:"card_id"`
	Title  string      `json:"title"`
	Rank   string      `json:"rank"`
	Items  []CheckItem `json:"items"`
}

type CheckItem struct {
	ID          int64  `json:"id"`
	ChecklistID int64  `json:"checklist_id"`
	Text        string `json:"text"`
	Done        bool   `json:"done"`
	Rank        string `json:"rank"`
}

type Comment struct {
	ID       int64  `json:"id"`
	CardID   int64  `json:"card_id"`
	UserID   int64  `json:"user_id"`
	UserName string `json:"user_name"`
	Body     string `json:"body"`
	BodyHTML string `json:"body_html"`
	Created  int64  `json:"created"`
	Edited   *int64 `json:"edited"`
}

// Snapshot is a whole board as one consistent read. Seq is the last event
// included, so a live stream opened with after=Seq misses nothing.
type Snapshot struct {
	Board   Board    `json:"board"`
	Lists   []List   `json:"lists"`
	Cards   []Card   `json:"cards"`
	Labels  []Label  `json:"labels"`
	Members []Member `json:"members"`
	Seq     int64    `json:"seq"`
}

type scanner interface{ Scan(dest ...any) error }

const boardCols = `b.id, b.title, b.color, b.created, b.archived_at, b.version,
	EXISTS (SELECT 1 FROM board_stars s WHERE s.board_id = b.id AND s.user_id = ?)`

func scanBoard(row scanner) (Board, error) {
	var b Board
	err := row.Scan(&b.ID, &b.Title, &b.Color, &b.Created, &b.ArchivedAt, &b.Version, &b.Starred)
	return b, err
}

const listCols = `id, board_id, title, rank, archived_at, version`

func scanList(row scanner) (List, error) {
	var l List
	err := row.Scan(&l.ID, &l.BoardID, &l.Title, &l.Rank, &l.ArchivedAt, &l.Version)
	return l, err
}

const cardCols = `c.id, c.board_id, c.list_id, c.title, c.rank, c.due, c.due_done, c.description <> '',
	c.updated, c.archived_at, c.version,
	(SELECT json_group_array(label_id) FROM card_labels WHERE card_id = c.id),
	(SELECT COUNT(*) FROM check_items i JOIN checklists k ON k.id = i.checklist_id WHERE k.card_id = c.id),
	(SELECT COUNT(*) FROM check_items i JOIN checklists k ON k.id = i.checklist_id WHERE k.card_id = c.id AND i.done),
	(SELECT COUNT(*) FROM comments WHERE card_id = c.id)`

func scanCard(row scanner) (Card, error) {
	var c Card
	var labels string
	err := row.Scan(&c.ID, &c.BoardID, &c.ListID, &c.Title, &c.Rank, &c.Due, &c.DueDone, &c.HasDescription,
		&c.Updated, &c.ArchivedAt, &c.Version, &labels, &c.CheckTotal, &c.CheckDone, &c.Comments)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(labels), &c.LabelIDs); err != nil {
		return c, err
	}
	return c, nil
}

// collect returns a function that scans every row with scan, so a query's
// (rows, err) can be passed straight in: collect(scanList)(db.QueryContext(...)).
func collect[T any](scan func(scanner) (T, error)) func(*sql.Rows, error) ([]T, error) {
	return func(rows *sql.Rows, err error) ([]T, error) {
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []T{}
		for rows.Next() {
			v, err := scan(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
}

func getBoard(ctx context.Context, q querier, userID, id int64) (Board, error) {
	b, err := scanBoard(q.QueryRowContext(ctx, `SELECT `+boardCols+` FROM boards b WHERE b.id = ?`, userID, id))
	return b, notFound(err)
}

func getList(ctx context.Context, q querier, id int64) (List, error) {
	l, err := scanList(q.QueryRowContext(ctx, `SELECT `+listCols+` FROM lists WHERE id = ?`, id))
	return l, notFound(err)
}

func getCard(ctx context.Context, q querier, id int64) (Card, error) {
	c, err := scanCard(q.QueryRowContext(ctx, `SELECT `+cardCols+` FROM cards c WHERE c.id = ?`, id))
	return c, notFound(err)
}

func getLabel(ctx context.Context, q querier, id int64) (Label, error) {
	var l Label
	err := q.QueryRowContext(ctx, `SELECT id, board_id, name, color FROM labels WHERE id = ?`, id).
		Scan(&l.ID, &l.BoardID, &l.Name, &l.Color)
	return l, notFound(err)
}

func getCardDetail(ctx context.Context, q querier, id int64) (CardDetail, error) {
	var d CardDetail
	c, err := getCard(ctx, q, id)
	if err != nil {
		return d, err
	}
	d.Card = c
	if err := q.QueryRowContext(ctx, `SELECT description, description_html, created FROM cards WHERE id = ?`, id).
		Scan(&d.Description, &d.DescriptionHTML, &d.Created); err != nil {
		return d, err
	}

	d.Checklists, err = collect(func(r scanner) (Checklist, error) {
		var k Checklist
		err := r.Scan(&k.ID, &k.CardID, &k.Title, &k.Rank)
		k.Items = []CheckItem{}
		return k, err
	})(q.QueryContext(ctx,
		`SELECT id, card_id, title, rank FROM checklists WHERE card_id = ? ORDER BY rank, id`, id))
	if err != nil {
		return d, err
	}
	items, err := collect(func(r scanner) (CheckItem, error) {
		var it CheckItem
		err := r.Scan(&it.ID, &it.ChecklistID, &it.Text, &it.Done, &it.Rank)
		return it, err
	})(q.QueryContext(ctx,
		`SELECT i.id, i.checklist_id, i.text, i.done, i.rank FROM check_items i
		 JOIN checklists k ON k.id = i.checklist_id WHERE k.card_id = ? ORDER BY i.rank, i.id`, id))
	if err != nil {
		return d, err
	}
	byID := map[int64]*Checklist{}
	for i := range d.Checklists {
		byID[d.Checklists[i].ID] = &d.Checklists[i]
	}
	for _, it := range items {
		k := byID[it.ChecklistID]
		k.Items = append(k.Items, it)
	}

	d.CommentList, err = collect(func(r scanner) (Comment, error) {
		var m Comment
		err := r.Scan(&m.ID, &m.CardID, &m.UserID, &m.UserName, &m.Body, &m.BodyHTML, &m.Created, &m.Edited)
		return m, err
	})(q.QueryContext(ctx,
		`SELECT m.id, m.card_id, m.user_id, u.name, m.body, m.body_html, m.created, m.edited
		 FROM comments m JOIN users u ON u.id = m.user_id WHERE m.card_id = ? ORDER BY m.created DESC, m.id DESC`, id))
	return d, err
}

// Boards lists the boards userID belongs to, starred first.
func (s *Store) Boards(ctx context.Context, userID int64) ([]Board, error) {
	return collect(scanBoard)(s.read.QueryContext(ctx, `SELECT `+boardCols+` FROM boards b
		JOIN board_members m ON m.board_id = b.id AND m.user_id = ?
		WHERE b.archived_at IS NULL
		ORDER BY 7 DESC, b.title COLLATE NOCASE, b.id`, userID, userID))
}

// BoardVersion returns the board's version, which changes on every write inside it.
func (s *Store) BoardVersion(ctx context.Context, id int64) (int64, error) {
	var v int64
	err := s.read.QueryRowContext(ctx, `SELECT version FROM boards WHERE id = ?`, id).Scan(&v)
	return v, notFound(err)
}

// Board reads a whole board in one read transaction.
func (s *Store) Board(ctx context.Context, userID, id int64) (*Snapshot, error) {
	tx, err := s.read.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	snap := &Snapshot{}
	if snap.Board, err = getBoard(ctx, tx, userID, id); err != nil {
		return nil, err
	}
	if snap.Lists, err = collect(scanList)(tx.QueryContext(ctx, `SELECT `+listCols+` FROM lists
		WHERE board_id = ? AND archived_at IS NULL ORDER BY rank, id`, id)); err != nil {
		return nil, err
	}
	if snap.Cards, err = collect(scanCard)(tx.QueryContext(ctx, `SELECT `+cardCols+` FROM cards c
		JOIN lists l ON l.id = c.list_id AND l.archived_at IS NULL
		WHERE c.board_id = ? AND c.archived_at IS NULL ORDER BY c.list_id, c.rank, c.id`, id)); err != nil {
		return nil, err
	}
	if snap.Labels, err = collect(func(r scanner) (Label, error) {
		var l Label
		err := r.Scan(&l.ID, &l.BoardID, &l.Name, &l.Color)
		return l, err
	})(tx.QueryContext(ctx,
		`SELECT id, board_id, name, color FROM labels WHERE board_id = ? ORDER BY id`, id)); err != nil {
		return nil, err
	}
	if snap.Members, err = collect(func(r scanner) (Member, error) {
		var m Member
		err := r.Scan(&m.UserID, &m.Name, &m.Role)
		return m, err
	})(tx.QueryContext(ctx, `SELECT u.id, u.name, m.role FROM board_members m
		JOIN users u ON u.id = m.user_id WHERE m.board_id = ? ORDER BY u.name`, id)); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE board_id = ?`, id).
		Scan(&snap.Seq); err != nil {
		return nil, err
	}
	return snap, nil
}

// CardDetail reads everything the card modal shows.
func (s *Store) CardDetail(ctx context.Context, id int64) (CardDetail, error) {
	tx, err := s.read.BeginTx(ctx, nil)
	if err != nil {
		return CardDetail{}, err
	}
	defer tx.Rollback()
	return getCardDetail(ctx, tx, id)
}

const eventCols = `e.seq, e.board_id, e.user_id, u.name, e.client_id, e.type, e.data, e.created`

func scanEvent(r scanner) (Event, error) {
	var e Event
	var data string
	err := r.Scan(&e.Seq, &e.BoardID, &e.UserID, &e.UserName, &e.ClientID, &e.Type, &data, &e.Created)
	e.Data = json.RawMessage(data)
	return e, err
}

// EventsAfter returns up to limit events with seq > after, oldest first.
func (s *Store) EventsAfter(ctx context.Context, boardID, after int64, limit int) ([]Event, error) {
	return collect(scanEvent)(s.read.QueryContext(ctx, `SELECT `+eventCols+` FROM events e JOIN users u ON u.id = e.user_id
		WHERE e.board_id = ? AND e.seq > ? ORDER BY e.seq LIMIT ?`, boardID, after, limit))
}

// EventsBefore returns up to limit events with seq < before (0 = latest), newest first.
func (s *Store) EventsBefore(ctx context.Context, boardID, before int64, limit int) ([]Event, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	return collect(scanEvent)(s.read.QueryContext(ctx, `SELECT `+eventCols+` FROM events e JOIN users u ON u.id = e.user_id
		WHERE e.board_id = ? AND e.seq < ? ORDER BY e.seq DESC LIMIT ?`, boardID, before, limit))
}
