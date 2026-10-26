package store

import "strings"

// Ranks are fractional indexes: strings that sort in display order under byte
// comparison (SQLite's default BINARY collation). There is always room for a new
// key between any two keys, so moving an item rewrites only that item's row.
// See SCALING.md §7.
//
// Invariant: no key ends with the smallest digit '0', which guarantees there is
// always room before any key.

const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func rankDigit(c byte) int { return strings.IndexByte(rankDigits, c) }

// rankBetween returns a key strictly between a and b. An empty a means "before
// everything"; an empty b means "after everything". It requires a < b when both
// are set.
func rankBetween(a, b string) string {
	if b == "" {
		return rankAfter(a)
	}
	return rankMidpoint(a, b)
}

// rankAfter returns a key greater than a, keeping keys short when items are
// appended one after another.
func rankAfter(a string) string {
	if a == "" {
		return "V"
	}
	for i := 0; i < len(a); i++ {
		if a[i] != 'z' {
			return a[:i] + string(rankDigits[rankDigit(a[i])+1])
		}
	}
	return a + "V"
}

// rankMidpoint returns a key roughly halfway between a and b, where an empty b
// is +infinity. Adapted from David Greenspan's "Implementing Fractional Indexing".
func rankMidpoint(a, b string) string {
	if b != "" {
		// Skip the common prefix, treating a as padded with '0'.
		n := 0
		for n < len(b) && rankAt(a, n) == b[n] {
			n++
		}
		if n > 0 {
			return b[:n] + rankMidpoint(rankSuffix(a, n), b[n:])
		}
	}
	da := 0
	if a != "" {
		da = rankDigit(a[0])
	}
	db := len(rankDigits)
	if b != "" {
		db = rankDigit(b[0])
	}
	if db-da > 1 {
		return string(rankDigits[(da+db+1)/2])
	}
	// The first digits are adjacent.
	if len(b) > 1 {
		return b[:1]
	}
	return string(rankDigits[da]) + rankMidpoint(rankSuffix(a, 1), "")
}

func rankAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return '0'
}

func rankSuffix(s string, n int) string {
	if n < len(s) {
		return s[n:]
	}
	return ""
}
