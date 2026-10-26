package store

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

var dev = Actor{UserID: 1, ClientID: "test"}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(evs []Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, evs...)
}

func (l *eventLog) types() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.events {
		out = append(out, e.Type)
	}
	return out
}

func open(t *testing.T) (*Store, *eventLog) {
	t.Helper()
	log := &eventLog{}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), log.add)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, log
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func cardTitles(t *testing.T, s *Store, boardID, listID int64) []string {
	t.Helper()
	snap := must[*Snapshot](t)(s.Board(context.Background(), 1, boardID))
	var out []string
	for _, c := range snap.Cards {
		if c.ListID == listID {
			out = append(out, c.Title)
		}
	}
	return out
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path, nil)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestBoardListCardFlow(t *testing.T) {
	ctx := context.Background()
	s, log := open(t)
	b := must[Board](t)(s.CreateBoard(ctx, dev, "  Roadmap  ", "green"))
	if b.Title != "Roadmap" || b.Color != "green" {
		t.Fatalf("board = %+v", b)
	}
	todo := must[List](t)(s.CreateList(ctx, dev, b.ID, "To do"))
	done := must[List](t)(s.CreateList(ctx, dev, b.ID, "Done"))
	for _, title := range []string{"a", "b", "c"} {
		must[Card](t)(s.CreateCard(ctx, dev, todo.ID, title))
	}
	snap := must[*Snapshot](t)(s.Board(ctx, 1, b.ID))
	if len(snap.Lists) != 2 || snap.Lists[0].ID != todo.ID || len(snap.Cards) != 3 || len(snap.Labels) != 6 {
		t.Fatalf("snapshot: %d lists, %d cards, %d labels", len(snap.Lists), len(snap.Cards), len(snap.Labels))
	}
	if snap.Seq == 0 {
		t.Fatal("snapshot seq not set")
	}
	byTitle := map[string]Card{}
	for _, c := range snap.Cards {
		byTitle[c.Title] = c
	}

	// c to the top, then a after c, then b into Done.
	must[Card](t)(s.MoveCard(ctx, dev, byTitle["c"].ID, todo.ID, 0, byTitle["a"].ID))
	if got := cardTitles(t, s, b.ID, todo.ID); !equal(got, "c", "a", "b") {
		t.Fatalf("after move to top: %v", got)
	}
	must[Card](t)(s.MoveCard(ctx, dev, byTitle["b"].ID, done.ID, 0, 0))
	if got := cardTitles(t, s, b.ID, todo.ID); !equal(got, "c", "a") {
		t.Fatalf("todo: %v", got)
	}
	if got := cardTitles(t, s, b.ID, done.ID); !equal(got, "b") {
		t.Fatalf("done: %v", got)
	}

	// Lists reorder.
	must[List](t)(s.MoveList(ctx, dev, done.ID, 0, todo.ID))
	snap = must[*Snapshot](t)(s.Board(ctx, 1, b.ID))
	if snap.Lists[0].ID != done.ID {
		t.Fatal("list move did not reorder")
	}

	// Archiving hides the card.
	must[CardDetail](t)(s.UpdateCard(ctx, dev, byTitle["a"].ID, CardPatch{Archived: ptr(true)}))
	if got := cardTitles(t, s, b.ID, todo.ID); !equal(got, "c") {
		t.Fatalf("after archive: %v", got)
	}

	want := []string{"board.created", "list.created", "list.created", "card.created", "card.created", "card.created",
		"card.moved", "card.moved", "list.moved", "card.archived"}
	if got := log.types(); !equal(got, want...) {
		t.Fatalf("events:\n got %v\nwant %v", got, want)
	}
}

func TestMoveNeighborValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	b := must[Board](t)(s.CreateBoard(ctx, dev, "B", ""))
	l1 := must[List](t)(s.CreateList(ctx, dev, b.ID, "1"))
	l2 := must[List](t)(s.CreateList(ctx, dev, b.ID, "2"))
	x := must[Card](t)(s.CreateCard(ctx, dev, l1.ID, "x"))
	y := must[Card](t)(s.CreateCard(ctx, dev, l2.ID, "y"))
	var inv *InvalidError
	if _, err := s.MoveCard(ctx, dev, x.ID, l1.ID, y.ID, 0); !errors.As(err, &inv) {
		t.Fatalf("neighbor in other list: err = %v", err)
	}
	if _, err := s.MoveCard(ctx, dev, x.ID, l1.ID, x.ID, 0); !errors.As(err, &inv) {
		t.Fatalf("self as neighbor: err = %v", err)
	}
	other := must[Board](t)(s.CreateBoard(ctx, dev, "Other", ""))
	ol := must[List](t)(s.CreateList(ctx, dev, other.ID, "o"))
	if _, err := s.MoveCard(ctx, dev, x.ID, ol.ID, 0, 0); !errors.As(err, &inv) {
		t.Fatalf("cross-board move: err = %v", err)
	}
}

func TestVersionConflict(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	b := must[Board](t)(s.CreateBoard(ctx, dev, "B", ""))
	l := must[List](t)(s.CreateList(ctx, dev, b.ID, "L"))
	c := must[Card](t)(s.CreateCard(ctx, dev, l.ID, "c"))
	stale := c.Version
	must[CardDetail](t)(s.UpdateCard(ctx, dev, c.ID, CardPatch{Title: ptr("c2"), Version: &stale}))
	if _, err := s.UpdateCard(ctx, dev, c.ID, CardPatch{Description: ptr("x"), Version: &stale}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: err = %v", err)
	}
}

func TestCardDetails(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	b := must[Board](t)(s.CreateBoard(ctx, dev, "B", ""))
	l := must[List](t)(s.CreateList(ctx, dev, b.ID, "L"))
	c := must[Card](t)(s.CreateCard(ctx, dev, l.ID, "c"))

	d := must[CardDetail](t)(s.UpdateCard(ctx, dev, c.ID, CardPatch{Description: ptr("**hi** <script>alert(1)</script>")}))
	if d.DescriptionHTML != "<p><strong>hi</strong> <!-- raw HTML omitted -->alert(1)<!-- raw HTML omitted --></p>\n" &&
		d.DescriptionHTML != "<p><strong>hi</strong> alert(1)</p>\n" {
		t.Fatalf("description_html = %q", d.DescriptionHTML)
	}
	if !d.HasDescription {
		t.Fatal("has_description false")
	}

	label := must[Label](t)(s.CreateLabel(ctx, dev, b.ID, "Bug", "red"))
	d = must[CardDetail](t)(s.SetCardLabel(ctx, dev, c.ID, label.ID, true))
	if len(d.LabelIDs) != 1 || d.LabelIDs[0] != label.ID {
		t.Fatalf("label_ids = %v", d.LabelIDs)
	}

	d = must[CardDetail](t)(s.CreateChecklist(ctx, dev, c.ID, ""))
	k := d.Checklists[0]
	d = must[CardDetail](t)(s.CreateCheckItem(ctx, dev, k.ID, "one"))
	d = must[CardDetail](t)(s.CreateCheckItem(ctx, dev, k.ID, "two"))
	d = must[CardDetail](t)(s.UpdateCheckItem(ctx, dev, d.Checklists[0].Items[0].ID, CheckItemPatch{Done: ptr(true)}))
	if d.CheckTotal != 2 || d.CheckDone != 1 || d.Checklists[0].Items[1].Text != "two" {
		t.Fatalf("checklist: %d/%d %+v", d.CheckDone, d.CheckTotal, d.Checklists)
	}

	d = must[CardDetail](t)(s.CreateComment(ctx, dev, c.ID, "looks *good*"))
	if d.Comments != 1 || d.CommentList[0].BodyHTML != "<p>looks <em>good</em></p>\n" {
		t.Fatalf("comments: %+v", d.CommentList)
	}

	due := int64(1_800_000_000_000)
	d = must[CardDetail](t)(s.UpdateCard(ctx, dev, c.ID, CardPatch{Due: Opt[int64]{Set: true, Value: &due}}))
	if d.Due == nil || *d.Due != due {
		t.Fatalf("due = %v", d.Due)
	}
	d = must[CardDetail](t)(s.UpdateCard(ctx, dev, c.ID, CardPatch{Due: Opt[int64]{Set: true}}))
	if d.Due != nil {
		t.Fatalf("due not cleared: %v", *d.Due)
	}

	must[CardDetail](t)(s.DeleteChecklist(ctx, dev, k.ID))
	if err := s.DeleteLabel(ctx, dev, label.ID); err != nil {
		t.Fatal(err)
	}
	d = must[CardDetail](t)(s.CardDetail(ctx, c.ID))
	if len(d.Checklists) != 0 || len(d.LabelIDs) != 0 || d.CheckTotal != 0 {
		t.Fatalf("after deletes: %+v", d)
	}
}

// TestConcurrentWritesBatch fires many writes at once: all must succeed, the
// final order must be consistent, and a failing write must not affect others.
func TestConcurrentWritesBatch(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	b := must[Board](t)(s.CreateBoard(ctx, dev, "B", ""))
	l := must[List](t)(s.CreateList(ctx, dev, b.ID, "L"))

	var wg sync.WaitGroup
	errs := make(chan error, 300)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CreateCard(ctx, dev, l.ID, "card"); err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.CreateCard(ctx, dev, 999999, "nope"); !errors.Is(err, ErrNotFound) {
				errs <- errors.New("bad list: want ErrNotFound")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	snap := must[*Snapshot](t)(s.Board(ctx, 1, b.ID))
	if len(snap.Cards) != 200 {
		t.Fatalf("got %d cards", len(snap.Cards))
	}
	ranks := map[string]bool{}
	for _, c := range snap.Cards {
		if ranks[c.Rank] {
			t.Fatalf("duplicate rank %q", c.Rank)
		}
		ranks[c.Rank] = true
	}

	// 100 concurrent moves of random cards to the top: order stays a permutation.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		c := snap.Cards[(i*37)%len(snap.Cards)]
		first := snap.Cards[0]
		go func() {
			defer wg.Done()
			if c.ID == first.ID {
				return
			}
			s.MoveCard(ctx, dev, c.ID, l.ID, 0, 0)
		}()
	}
	wg.Wait()
	snap = must[*Snapshot](t)(s.Board(ctx, 1, b.ID))
	keys := make([]string, len(snap.Cards))
	for i, c := range snap.Cards {
		keys[i] = c.Rank
	}
	if len(keys) != 200 || !sort.StringsAreSorted(keys) {
		t.Fatal("order broken after concurrent moves")
	}
}

func TestPanicInWriteIsIsolated(t *testing.T) {
	ctx := context.Background()
	s, _ := open(t)
	err := s.w.Do(ctx, dev, func(tx *Tx) error { panic("boom") })
	if err == nil {
		t.Fatal("want error from panicking write")
	}
	if _, err := s.CreateBoard(ctx, dev, "still works", ""); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

func equal(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
