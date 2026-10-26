package store

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
)

func checkKey(t *testing.T, k string) {
	t.Helper()
	if k == "" || strings.HasSuffix(k, "0") {
		t.Fatalf("bad key %q", k)
	}
	for i := 0; i < len(k); i++ {
		if rankDigit(k[i]) < 0 {
			t.Fatalf("key %q has invalid digit %q", k, k[i])
		}
	}
}

func TestRankBetween(t *testing.T) {
	cases := [][2]string{{"", ""}, {"", "V"}, {"V", ""}, {"V", "W"}, {"V", "V1"}, {"0V", "1"}, {"z", ""}, {"", "01"}, {"zzz", ""}}
	for _, c := range cases {
		k := rankBetween(c[0], c[1])
		checkKey(t, k)
		if c[0] != "" && k <= c[0] {
			t.Errorf("rankBetween(%q, %q) = %q, not after a", c[0], c[1], k)
		}
		if c[1] != "" && k >= c[1] {
			t.Errorf("rankBetween(%q, %q) = %q, not before b", c[0], c[1], k)
		}
	}
}

// TestRankRandomInserts inserts at random positions and checks that the keys
// always stay strictly ordered and valid.
func TestRankRandomInserts(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var keys []string
	for i := 0; i < 5000; i++ {
		pos := r.Intn(len(keys) + 1)
		var a, b string
		if pos > 0 {
			a = keys[pos-1]
		}
		if pos < len(keys) {
			b = keys[pos]
		}
		k := rankBetween(a, b)
		checkKey(t, k)
		if (a != "" && k <= a) || (b != "" && k >= b) {
			t.Fatalf("insert %d: rankBetween(%q, %q) = %q", i, a, b, k)
		}
		keys = append(keys[:pos], append([]string{k}, keys[pos:]...)...)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys not sorted")
	}
}

func TestRankAppendStaysShort(t *testing.T) {
	k := ""
	for i := 0; i < 1000; i++ {
		k = rankAfter(k)
	}
	if len(k) > 40 {
		t.Errorf("after 1000 appends key is %d chars: %q", len(k), k)
	}
}

func TestRankRepeatedSameSpot(t *testing.T) {
	// Dropping a card into the same gap over and over grows keys slowly.
	a, b := "V", "W"
	for i := 0; i < 200; i++ {
		k := rankBetween(a, b)
		checkKey(t, k)
		if k <= a || k >= b {
			t.Fatalf("rankBetween(%q, %q) = %q", a, b, k)
		}
		b = k
	}
	if len(b) > 50 {
		t.Errorf("key grew to %d chars", len(b))
	}
}
