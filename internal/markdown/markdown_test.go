package markdown

import (
	"strings"
	"testing"
)

func TestRenderIsInert(t *testing.T) {
	attacks := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[click](javascript:alert(1))`,
		`<a href="javascript:alert(1)">x</a>`,
		`<iframe src="https://example.com"></iframe>`,
		`![x](javascript:alert(1))`,
		`<div style="background:url(javascript:alert(1))">x</div>`,
		"- [ ] <input type=text onfocus=alert(1) autofocus>",
	}
	for _, a := range attacks {
		out := strings.ToLower(Render(a))
		for _, bad := range []string{"<script", "onerror", "onfocus", "javascript:", "<iframe", "style=", `type="text"`} {
			if strings.Contains(out, bad) {
				t.Errorf("Render(%q) = %q contains %q", a, out, bad)
			}
		}
	}
}

func TestRenderFeatures(t *testing.T) {
	cases := map[string]string{
		"**bold**":              "<strong>bold</strong>",
		"line one\nline two":    "<br",
		"- [x] done":            `<input checked="" disabled="" type="checkbox"`,
		"https://example.com":   `href="https://example.com"`,
		"[a](https://x.test)":   `target="_blank"`,
		"~~gone~~":              "<del>gone</del>",
		"| a |\n|---|\n| b |\n": "<table>",
	}
	for in, want := range cases {
		if out := Render(in); !strings.Contains(out, want) {
			t.Errorf("Render(%q) = %q, want it to contain %q", in, out, want)
		}
	}
}
