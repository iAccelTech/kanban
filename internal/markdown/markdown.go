// Package markdown renders user-written Markdown to HTML that is safe to insert
// into a page.
package markdown

import (
	"bytes"
	"regexp"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

var (
	// Raw HTML in the source is dropped by goldmark (no html.WithUnsafe), and
	// whatever comes out is sanitized again: two independent layers.
	md = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	policy = newPolicy()
)

func newPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AddTargetBlankToFullyQualifiedLinks(true)
	// GFM task lists render as disabled checkboxes.
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	return p
}

// Render converts Markdown source to sanitized HTML.
func Render(src string) string {
	if src == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		// goldmark only fails on writer errors, which a bytes.Buffer never returns.
		return policy.Sanitize(src)
	}
	return string(policy.SanitizeBytes(buf.Bytes()))
}
