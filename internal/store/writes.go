package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/iAccelTech/kanban/internal/markdown"
)

var bg = context.Background() // write functions run on the writer goroutine, not the request

// place returns a rank that puts selfID in the given scope immediately after
// beforeID (the item above it), or else immediately before afterID (the item
// below it), or else at the end. Neighbors are looked up on the server, so a
// client whose view is slightly stale still gets a consistent order.
func place(tx *Tx, table, scopeCol string, scopeID, selfID, beforeID, afterID int64) (string, error) {
	live := ""
	if table != "check_items" && table != "checklists" {
		live = " AND archived_at IS NULL"
	}
	where := fmt.Sprintf("%s = ? AND id <> ?%s", scopeCol, live)
	var a, b string
	switch {
	case beforeID != 0:
		err := tx.QueryRow(fmt.Sprintf(`SELECT rank FROM %s WHERE id = ? AND %s`, table, where),
			beforeID, scopeID, selfID).Scan(&a)
		if errors.Is(err, sql.ErrNoRows) {
			return "", invalid("before_id %d is not in the target list", beforeID)
		} else if err != nil {
			return "", err
		}
		err = tx.QueryRow(fmt.Sprintf(`SELECT COALESCE(MIN(rank), '') FROM %s WHERE %s AND rank > ?`, table, where),
			scopeID, selfID, a).Scan(&b)
		if err != nil {
			return "", err
		}
	case afterID != 0:
		err := tx.QueryRow(fmt.Sprintf(`SELECT rank FROM %s WHERE id = ? AND %s`, table, where),
			afterID, scopeID, selfID).Scan(&b)
		if errors.Is(err, sql.ErrNoRows) {
			return "", invalid("after_id %d is not in the target list", afterID)
		} else if err != nil {
			return "", err
		}
		err = tx.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(rank), '') FROM %s WHERE %s AND rank < ?`, table, where),
			scopeID, selfID, b).Scan(&a)
		if err != nil {
			return "", err
		}
	default:
		err := tx.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(rank), '') FROM %s WHERE %s`, table, where),
			scopeID, selfID).Scan(&a)
		if err != nil {
			return "", err
		}
	}
	return rankBetween(a, b), nil
}

// ---- Boards ----

var defaultLabels = []string{"green", "yellow", "orange", "red", "purple", "blue"}

func (s *Store) CreateBoard(ctx context.Context, a Actor, title, color string) (Board, error) {
	title, err := cleanLine("title", title, 1, 200)
	if err != nil {
		return Board{}, err
	}
	if color == "" {
		color = "blue"
	}
	if err := checkColor("color", color, BoardColors); err != nil {
		return Board{}, err
	}
	var out Board
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		now := nowMS()
		res, err := tx.Exec(`INSERT INTO boards (workspace_id, title, color, created_by, created) VALUES (1, ?, ?, ?, ?)`,
			title, color, a.UserID, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO board_members (board_id, user_id, role) VALUES (?, ?, 'owner')`, id, a.UserID); err != nil {
			return err
		}
		for _, c := range defaultLabels {
			if _, err := tx.Exec(`INSERT INTO labels (board_id, color) VALUES (?, ?)`, id, c); err != nil {
				return err
			}
		}
		b, err := getBoard(bg, tx, a.UserID, id)
		if err != nil {
			return err
		}
		if err := tx.emit(id, "board.created", map[string]any{"board": b}); err != nil {
			return err
		}
		out, err = getBoard(bg, tx, a.UserID, id)
		return err
	})
	return out, err
}

type BoardPatch struct {
	Title    *string `json:"title"`
	Color    *string `json:"color"`
	Archived *bool   `json:"archived"`
}

func (s *Store) UpdateBoard(ctx context.Context, a Actor, id int64, p BoardPatch) (Board, error) {
	var title string
	var err error
	if p.Title != nil {
		if title, err = cleanLine("title", *p.Title, 1, 200); err != nil {
			return Board{}, err
		}
	}
	if p.Color != nil {
		if err := checkColor("color", *p.Color, BoardColors); err != nil {
			return Board{}, err
		}
	}
	var out Board
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		b, err := getBoard(bg, tx, a.UserID, id)
		if err != nil {
			return err
		}
		oldTitle := b.Title
		type change struct {
			typ, sql string
			arg      any
		}
		var changes []change
		if p.Title != nil && title != b.Title {
			changes = append(changes, change{"board.renamed", `title = ?`, title})
		}
		if p.Color != nil && *p.Color != b.Color {
			changes = append(changes, change{"board.recolored", `color = ?`, *p.Color})
		}
		if p.Archived != nil && *p.Archived != (b.ArchivedAt != nil) {
			if *p.Archived {
				changes = append(changes, change{"board.archived", `archived_at = ?`, nowMS()})
			} else {
				changes = append(changes, change{"board.reopened", `archived_at = ?`, nil})
			}
		}
		for _, c := range changes {
			if _, err := tx.Exec(`UPDATE boards SET `+c.sql+` WHERE id = ?`, c.arg, id); err != nil {
				return err
			}
			b, err := getBoard(bg, tx, a.UserID, id)
			if err != nil {
				return err
			}
			if err := tx.emit(id, c.typ, map[string]any{"board": b, "from": oldTitle}); err != nil {
				return err
			}
		}
		out, err = getBoard(bg, tx, a.UserID, id)
		return err
	})
	return out, err
}

// SetStar stars or unstars a board for the actor. Stars are personal, so no event.
func (s *Store) SetStar(ctx context.Context, a Actor, boardID int64, on bool) error {
	return s.w.Do(ctx, a, func(tx *Tx) error {
		if _, err := getBoard(bg, tx, a.UserID, boardID); err != nil {
			return err
		}
		var err error
		if on {
			_, err = tx.Exec(`INSERT OR IGNORE INTO board_stars (user_id, board_id) VALUES (?, ?)`, a.UserID, boardID)
		} else {
			_, err = tx.Exec(`DELETE FROM board_stars WHERE user_id = ? AND board_id = ?`, a.UserID, boardID)
		}
		return err
	})
}

// ---- Lists ----

func (s *Store) CreateList(ctx context.Context, a Actor, boardID int64, title string) (List, error) {
	title, err := cleanLine("title", title, 1, 200)
	if err != nil {
		return List{}, err
	}
	var out List
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		if _, err := getBoard(bg, tx, a.UserID, boardID); err != nil {
			return err
		}
		rank, err := place(tx, "lists", "board_id", boardID, 0, 0, 0)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO lists (board_id, title, rank, created) VALUES (?, ?, ?, ?)`,
			boardID, title, rank, nowMS())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if out, err = getList(bg, tx, id); err != nil {
			return err
		}
		return tx.emit(boardID, "list.created", map[string]any{"list": out})
	})
	return out, err
}

type ListPatch struct {
	Title    *string `json:"title"`
	Archived *bool   `json:"archived"`
}

func (s *Store) UpdateList(ctx context.Context, a Actor, id int64, p ListPatch) (List, error) {
	var title string
	var err error
	if p.Title != nil {
		if title, err = cleanLine("title", *p.Title, 1, 200); err != nil {
			return List{}, err
		}
	}
	var out List
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		l, err := getList(bg, tx, id)
		if err != nil {
			return err
		}
		from := l.Title
		if p.Title != nil && title != l.Title {
			if _, err := tx.Exec(`UPDATE lists SET title = ?, version = version + 1 WHERE id = ?`, title, id); err != nil {
				return err
			}
			if l, err = getList(bg, tx, id); err != nil {
				return err
			}
			if err := tx.emit(l.BoardID, "list.renamed", map[string]any{"list": l, "from": from}); err != nil {
				return err
			}
		}
		if p.Archived != nil && *p.Archived != (l.ArchivedAt != nil) {
			typ := "list.archived"
			if *p.Archived {
				_, err = tx.Exec(`UPDATE lists SET archived_at = ?, version = version + 1 WHERE id = ?`, nowMS(), id)
			} else {
				typ = "list.restored"
				var rank string
				if rank, err = place(tx, "lists", "board_id", l.BoardID, id, 0, 0); err == nil {
					_, err = tx.Exec(`UPDATE lists SET archived_at = NULL, rank = ?, version = version + 1 WHERE id = ?`, rank, id)
				}
			}
			if err != nil {
				return err
			}
			if l, err = getList(bg, tx, id); err != nil {
				return err
			}
			if err := tx.emit(l.BoardID, typ, map[string]any{"list": l}); err != nil {
				return err
			}
		}
		out = l
		return nil
	})
	return out, err
}

// MoveList puts a list right after beforeID, or right before afterID, or last.
func (s *Store) MoveList(ctx context.Context, a Actor, id, beforeID, afterID int64) (List, error) {
	var out List
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		l, err := getList(bg, tx, id)
		if err != nil {
			return err
		}
		rank, err := place(tx, "lists", "board_id", l.BoardID, id, beforeID, afterID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE lists SET rank = ?, version = version + 1 WHERE id = ?`, rank, id); err != nil {
			return err
		}
		if out, err = getList(bg, tx, id); err != nil {
			return err
		}
		return tx.emit(l.BoardID, "list.moved", map[string]any{"list": out})
	})
	return out, err
}

// ---- Cards ----

func (s *Store) CreateCard(ctx context.Context, a Actor, listID int64, title string) (Card, error) {
	title, err := cleanLine("title", title, 1, 512)
	if err != nil {
		return Card{}, err
	}
	var out Card
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		l, err := getList(bg, tx, listID)
		if err != nil {
			return err
		}
		if l.ArchivedAt != nil {
			return invalid("list is archived")
		}
		rank, err := place(tx, "cards", "list_id", listID, 0, 0, 0)
		if err != nil {
			return err
		}
		now := nowMS()
		res, err := tx.Exec(`INSERT INTO cards (board_id, list_id, title, rank, created_by, created, updated)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, l.BoardID, listID, title, rank, a.UserID, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if out, err = getCard(bg, tx, id); err != nil {
			return err
		}
		return tx.emit(l.BoardID, "card.created", map[string]any{"card": out, "list_title": l.Title})
	})
	return out, err
}

type CardPatch struct {
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	Due         Opt[int64] `json:"due"`
	DueDone     *bool      `json:"due_done"`
	Archived    *bool      `json:"archived"`
	// Version, if set, makes the update fail with ErrConflict unless the card is
	// still at this version.
	Version *int64 `json:"version"`
}

func (s *Store) UpdateCard(ctx context.Context, a Actor, id int64, p CardPatch) (CardDetail, error) {
	var title, desc string
	var err error
	if p.Title != nil {
		if title, err = cleanLine("title", *p.Title, 1, 512); err != nil {
			return CardDetail{}, err
		}
	}
	if p.Description != nil {
		if desc, err = cleanText("description", *p.Description, 16384); err != nil {
			return CardDetail{}, err
		}
	}
	descHTML := markdown.Render(desc) // outside the writer: rendering is CPU work

	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		c, err := getCard(bg, tx, id)
		if err != nil {
			return err
		}
		if p.Version != nil && *p.Version != c.Version {
			return ErrConflict
		}
		set := func(typ, sql string, data map[string]any, args ...any) error {
			args = append(args, nowMS(), id)
			if _, err := tx.Exec(`UPDATE cards SET `+sql+`, updated = ?, version = version + 1 WHERE id = ?`, args...); err != nil {
				return err
			}
			card, err := getCard(bg, tx, id)
			if err != nil {
				return err
			}
			if data == nil {
				data = map[string]any{}
			}
			data["card"] = card
			return tx.emit(c.BoardID, typ, data)
		}
		if p.Title != nil && title != c.Title {
			if err := set("card.renamed", `title = ?`, map[string]any{"from": c.Title}, title); err != nil {
				return err
			}
		}
		if p.Description != nil {
			var old string
			if err := tx.QueryRow(`SELECT description FROM cards WHERE id = ?`, id).Scan(&old); err != nil {
				return err
			}
			if desc != old {
				if err := set("card.described", `description = ?, description_html = ?`, nil, desc, descHTML); err != nil {
					return err
				}
			}
		}
		if p.Due.Set && !sameDue(p.Due.Value, c.Due) {
			if err := set("card.due", `due = ?, due_done = 0`, nil, p.Due.Value); err != nil {
				return err
			}
		}
		if p.DueDone != nil && *p.DueDone != c.DueDone {
			if err := set("card.due_done", `due_done = ?`, nil, *p.DueDone); err != nil {
				return err
			}
		}
		if p.Archived != nil && *p.Archived != (c.ArchivedAt != nil) {
			if *p.Archived {
				err = set("card.archived", `archived_at = ?`, nil, nowMS())
			} else {
				var rank string
				if rank, err = place(tx, "cards", "list_id", c.ListID, id, 0, 0); err == nil {
					err = set("card.restored", `archived_at = NULL, rank = ?`, nil, rank)
				}
			}
			if err != nil {
				return err
			}
		}
		out, err = getCardDetail(bg, tx, id)
		return err
	})
	return out, err
}

func sameDue(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// MoveCard puts a card in listID right after beforeID (the card above it), or
// right before afterID (the card below it), or at the end of the list.
func (s *Store) MoveCard(ctx context.Context, a Actor, id, listID, beforeID, afterID int64) (Card, error) {
	var out Card
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		c, err := getCard(bg, tx, id)
		if err != nil {
			return err
		}
		if listID == 0 {
			listID = c.ListID
		}
		to, err := getList(bg, tx, listID)
		if err != nil {
			return err
		}
		if to.BoardID != c.BoardID {
			return invalid("cards can only move within their board")
		}
		if to.ArchivedAt != nil {
			return invalid("list is archived")
		}
		rank, err := place(tx, "cards", "list_id", listID, id, beforeID, afterID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE cards SET list_id = ?, rank = ?, updated = ?, version = version + 1 WHERE id = ?`,
			listID, rank, nowMS(), id); err != nil {
			return err
		}
		if out, err = getCard(bg, tx, id); err != nil {
			return err
		}
		data := map[string]any{"card": out}
		if listID != c.ListID {
			from, err := getList(bg, tx, c.ListID)
			if err != nil {
				return err
			}
			data["from_list_id"], data["from_list_title"], data["to_list_title"] = c.ListID, from.Title, to.Title
		}
		return tx.emit(c.BoardID, "card.moved", data)
	})
	return out, err
}

// ---- Labels ----

func (s *Store) CreateLabel(ctx context.Context, a Actor, boardID int64, name, color string) (Label, error) {
	name, err := cleanLine("name", name, 0, 64)
	if err != nil {
		return Label{}, err
	}
	if err := checkColor("color", color, LabelColors); err != nil {
		return Label{}, err
	}
	var out Label
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		if _, err := getBoard(bg, tx, a.UserID, boardID); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO labels (board_id, name, color) VALUES (?, ?, ?)`, boardID, name, color)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if out, err = getLabel(bg, tx, id); err != nil {
			return err
		}
		return tx.emit(boardID, "label.created", map[string]any{"label": out})
	})
	return out, err
}

type LabelPatch struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

func (s *Store) UpdateLabel(ctx context.Context, a Actor, id int64, p LabelPatch) (Label, error) {
	var name string
	var err error
	if p.Name != nil {
		if name, err = cleanLine("name", *p.Name, 0, 64); err != nil {
			return Label{}, err
		}
	}
	if p.Color != nil {
		if err := checkColor("color", *p.Color, LabelColors); err != nil {
			return Label{}, err
		}
	}
	var out Label
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		l, err := getLabel(bg, tx, id)
		if err != nil {
			return err
		}
		if p.Name != nil {
			l.Name = name
		}
		if p.Color != nil {
			l.Color = *p.Color
		}
		if _, err := tx.Exec(`UPDATE labels SET name = ?, color = ? WHERE id = ?`, l.Name, l.Color, id); err != nil {
			return err
		}
		out = l
		return tx.emit(l.BoardID, "label.updated", map[string]any{"label": l})
	})
	return out, err
}

func (s *Store) DeleteLabel(ctx context.Context, a Actor, id int64) error {
	return s.w.Do(ctx, a, func(tx *Tx) error {
		l, err := getLabel(bg, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM labels WHERE id = ?`, id); err != nil {
			return err
		}
		return tx.emit(l.BoardID, "label.deleted", map[string]any{"label_id": id})
	})
}

// SetCardLabel adds or removes a label on a card.
func (s *Store) SetCardLabel(ctx context.Context, a Actor, cardID, labelID int64, on bool) (CardDetail, error) {
	var out CardDetail
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		c, err := getCard(bg, tx, cardID)
		if err != nil {
			return err
		}
		l, err := getLabel(bg, tx, labelID)
		if err != nil {
			return err
		}
		if l.BoardID != c.BoardID {
			return invalid("label belongs to another board")
		}
		var res sql.Result
		if on {
			res, err = tx.Exec(`INSERT OR IGNORE INTO card_labels (card_id, label_id) VALUES (?, ?)`, cardID, labelID)
		} else {
			res, err = tx.Exec(`DELETE FROM card_labels WHERE card_id = ? AND label_id = ?`, cardID, labelID)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			card, err := getCard(bg, tx, cardID)
			if err != nil {
				return err
			}
			if err := tx.emit(c.BoardID, "card.labeled", map[string]any{"card": card, "label": l, "on": on}); err != nil {
				return err
			}
		}
		out, err = getCardDetail(bg, tx, cardID)
		return err
	})
	return out, err
}

// ---- Checklists ----

// cardChanged emits a card-level event after a change to a card's checklists or
// comments, so boards refresh the card's badges.
func cardChanged(tx *Tx, cardID int64, typ string, data map[string]any) (CardDetail, error) {
	d, err := getCardDetail(bg, tx, cardID)
	if err != nil {
		return d, err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["card"] = d.Card
	return d, tx.emit(d.BoardID, typ, data)
}

func checklistCard(tx *Tx, checklistID int64) (int64, error) {
	var cardID int64
	err := tx.QueryRow(`SELECT card_id FROM checklists WHERE id = ?`, checklistID).Scan(&cardID)
	return cardID, notFound(err)
}

func itemCard(tx *Tx, itemID int64) (cardID, checklistID int64, err error) {
	err = tx.QueryRow(`SELECT k.card_id, k.id FROM check_items i JOIN checklists k ON k.id = i.checklist_id WHERE i.id = ?`,
		itemID).Scan(&cardID, &checklistID)
	return cardID, checklistID, notFound(err)
}

func (s *Store) CreateChecklist(ctx context.Context, a Actor, cardID int64, title string) (CardDetail, error) {
	if title == "" {
		title = "Checklist"
	}
	title, err := cleanLine("title", title, 1, 200)
	if err != nil {
		return CardDetail{}, err
	}
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		if _, err := getCard(bg, tx, cardID); err != nil {
			return err
		}
		rank, err := place(tx, "checklists", "card_id", cardID, 0, 0, 0)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO checklists (card_id, title, rank) VALUES (?, ?, ?)`, cardID, title, rank); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "checklist.created", map[string]any{"title": title})
		return err
	})
	return out, err
}

func (s *Store) RenameChecklist(ctx context.Context, a Actor, id int64, title string) (CardDetail, error) {
	title, err := cleanLine("title", title, 1, 200)
	if err != nil {
		return CardDetail{}, err
	}
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, err := checklistCard(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE checklists SET title = ? WHERE id = ?`, title, id); err != nil {
			return err
		}
		out, err = getCardDetail(bg, tx, cardID)
		return err
	})
	return out, err
}

func (s *Store) DeleteChecklist(ctx context.Context, a Actor, id int64) (CardDetail, error) {
	var out CardDetail
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, err := checklistCard(tx, id)
		if err != nil {
			return err
		}
		var title string
		if err := tx.QueryRow(`SELECT title FROM checklists WHERE id = ?`, id).Scan(&title); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM checklists WHERE id = ?`, id); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "checklist.deleted", map[string]any{"title": title})
		return err
	})
	return out, err
}

func (s *Store) CreateCheckItem(ctx context.Context, a Actor, checklistID int64, text string) (CardDetail, error) {
	text, err := cleanLine("text", text, 1, 512)
	if err != nil {
		return CardDetail{}, err
	}
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, err := checklistCard(tx, checklistID)
		if err != nil {
			return err
		}
		rank, err := place(tx, "check_items", "checklist_id", checklistID, 0, 0, 0)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO check_items (checklist_id, text, rank) VALUES (?, ?, ?)`, checklistID, text, rank); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "checkitem.created", map[string]any{"text": text})
		return err
	})
	return out, err
}

type CheckItemPatch struct {
	Text *string `json:"text"`
	Done *bool   `json:"done"`
}

func (s *Store) UpdateCheckItem(ctx context.Context, a Actor, id int64, p CheckItemPatch) (CardDetail, error) {
	var text string
	var err error
	if p.Text != nil {
		if text, err = cleanLine("text", *p.Text, 1, 512); err != nil {
			return CardDetail{}, err
		}
	}
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, _, err := itemCard(tx, id)
		if err != nil {
			return err
		}
		if p.Text != nil {
			if _, err := tx.Exec(`UPDATE check_items SET text = ? WHERE id = ?`, text, id); err != nil {
				return err
			}
		}
		if p.Done != nil {
			res, err := tx.Exec(`UPDATE check_items SET done = ? WHERE id = ? AND done <> ?`, *p.Done, id, *p.Done)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if err := tx.QueryRow(`SELECT text FROM check_items WHERE id = ?`, id).Scan(&text); err != nil {
					return err
				}
				typ := "checkitem.completed"
				if !*p.Done {
					typ = "checkitem.uncompleted"
				}
				out, err = cardChanged(tx, cardID, typ, map[string]any{"text": text})
				return err
			}
		}
		out, err = getCardDetail(bg, tx, cardID)
		return err
	})
	return out, err
}

func (s *Store) DeleteCheckItem(ctx context.Context, a Actor, id int64) (CardDetail, error) {
	var out CardDetail
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, _, err := itemCard(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM check_items WHERE id = ?`, id); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "checkitem.deleted", nil)
		return err
	})
	return out, err
}

// ---- Comments ----

func excerpt(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

func (s *Store) CreateComment(ctx context.Context, a Actor, cardID int64, body string) (CardDetail, error) {
	body, err := cleanText("comment", body, 16384)
	if err != nil {
		return CardDetail{}, err
	}
	if body == "" {
		return CardDetail{}, invalid("comment is required")
	}
	html := markdown.Render(body)
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		if _, err := getCard(bg, tx, cardID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO comments (card_id, user_id, body, body_html, created) VALUES (?, ?, ?, ?, ?)`,
			cardID, a.UserID, body, html, nowMS()); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "comment.created", map[string]any{"excerpt": excerpt(body)})
		return err
	})
	return out, err
}

// commentCard returns the comment's card, and ErrNotFound unless the actor wrote it.
func commentCard(tx *Tx, a Actor, id int64) (int64, error) {
	var cardID, userID int64
	if err := tx.QueryRow(`SELECT card_id, user_id FROM comments WHERE id = ?`, id).Scan(&cardID, &userID); err != nil {
		return 0, notFound(err)
	}
	if userID != a.UserID {
		return 0, ErrNotFound
	}
	return cardID, nil
}

func (s *Store) UpdateComment(ctx context.Context, a Actor, id int64, body string) (CardDetail, error) {
	body, err := cleanText("comment", body, 16384)
	if err != nil {
		return CardDetail{}, err
	}
	if body == "" {
		return CardDetail{}, invalid("comment is required")
	}
	html := markdown.Render(body)
	var out CardDetail
	err = s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, err := commentCard(tx, a, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE comments SET body = ?, body_html = ?, edited = ? WHERE id = ?`, body, html, nowMS(), id); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "comment.edited", nil)
		return err
	})
	return out, err
}

func (s *Store) DeleteComment(ctx context.Context, a Actor, id int64) (CardDetail, error) {
	var out CardDetail
	err := s.w.Do(ctx, a, func(tx *Tx) error {
		cardID, err := commentCard(tx, a, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM comments WHERE id = ?`, id); err != nil {
			return err
		}
		out, err = cardChanged(tx, cardID, "comment.deleted", nil)
		return err
	})
	return out, err
}
