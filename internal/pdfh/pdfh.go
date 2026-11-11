// Package pdfh implements the Haiku-style selectors drpy calls pdfh, pdfa
// and pd. Rules chain steps with "&&", take the nth match with ":eq(n)",
// and fall back with "||". The last step is Text, Html, an attribute, or
// another selector (whose text is returned).
package pdfh

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
)

var eqRe = regexp.MustCompile(`:eq\((-?\d+)\)`)

var tags = map[string]bool{
	"a": true, "div": true, "span": true, "p": true, "li": true, "ul": true, "ol": true,
	"img": true, "script": true, "body": true, "html": true, "title": true, "em": true,
	"strong": true, "td": true, "tr": true, "table": true, "video": true, "source": true,
	"i": true, "b": true, "label": true, "header": true, "footer": true, "section": true,
	"article": true, "nav": true, "input": true, "meta": true, "link": true, "head": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// PDFH returns the first non-empty match of expr in html.
func PDFH(html, expr string) string {
	expr = strings.TrimSpace(expr)
	if html == "" || expr == "" {
		return ""
	}
	for _, part := range strings.Split(expr, "||") {
		if v := pdfhOne(html, strings.TrimSpace(part)); v != "" {
			return v
		}
	}
	return ""
}

// PDFA returns the outer HTML of each element expr matches. An extraction
// step (Text, Html, attribute) maps that extraction over the matches.
func PDFA(html, expr string) []string {
	expr = strings.TrimSpace(expr)
	if html == "" || expr == "" {
		return []string{}
	}
	for _, part := range strings.Split(expr, "||") {
		if got := pdfaOne(html, strings.TrimSpace(part)); len(got) > 0 {
			return got
		}
	}
	return []string{}
}

// PD is PDFH followed by joining a relative result onto base.
func PD(html, expr, base string) string {
	v := PDFH(html, expr)
	if v == "" || base == "" {
		return v
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "magnet:") {
		return v
	}
	return urljoin.Join(base, v)
}

func pdfhOne(html, expr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}
	sel := doc.Selection
	segs := splitAnd(expr)
	for i, seg := range segs {
		selExpr, eq := splitEq(seg)
		last := i == len(segs)-1
		if last && isExtract(selExpr) {
			sel = narrow(sel, eq, true)
			return extract(sel, selExpr)
		}
		if selExpr != "" {
			sel = sel.Find(selExpr)
		}
		if eq != nil {
			sel = at(sel, *eq)
		} else if last {
			sel = sel.Eq(0)
		}
		if last {
			return strings.TrimSpace(sel.Text())
		}
	}
	return ""
}

func pdfaOne(html, expr string) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	sel := doc.Selection
	segs := splitAnd(expr)
	for i, seg := range segs {
		selExpr, eq := splitEq(seg)
		last := i == len(segs)-1
		if last && isExtract(selExpr) {
			if eq != nil {
				sel = at(sel, *eq)
			}
			var out []string
			sel.Each(func(_ int, s *goquery.Selection) {
				out = append(out, extract(s, selExpr))
			})
			return out
		}
		if selExpr != "" {
			sel = sel.Find(selExpr)
		}
		if eq != nil {
			sel = at(sel, *eq)
		}
		if last {
			var out []string
			sel.Each(func(_ int, s *goquery.Selection) {
				h, err := goquery.OuterHtml(s)
				if err == nil {
					out = append(out, h)
				}
			})
			return out
		}
	}
	return nil
}

func splitAnd(expr string) []string {
	parts := strings.Split(expr, "&&")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func splitEq(seg string) (string, *int) {
	m := eqRe.FindStringSubmatchIndex(seg)
	if m == nil {
		return seg, nil
	}
	n, err := strconv.Atoi(seg[m[2]:m[3]])
	if err != nil {
		return seg, nil
	}
	sel := strings.TrimSpace(seg[:m[0]] + seg[m[1]:])
	return sel, &n
}

func isExtract(s string) bool {
	if s == "Text" || s == "Html" {
		return true
	}
	if s == "" || tags[s] {
		return false
	}
	if strings.ContainsAny(s, ".#>[ ") {
		return false
	}
	for _, r := range s {
		if r == '-' || r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func narrow(sel *goquery.Selection, eq *int, first bool) *goquery.Selection {
	if eq != nil {
		return at(sel, *eq)
	}
	if first {
		return sel.Eq(0)
	}
	return sel
}

func at(sel *goquery.Selection, n int) *goquery.Selection {
	if n < 0 {
		n = sel.Length() + n
	}
	if n < 0 {
		return sel.Eq(sel.Length())
	}
	return sel.Eq(n)
}

func extract(sel *goquery.Selection, kind string) string {
	if sel.Length() == 0 {
		return ""
	}
	switch kind {
	case "Text":
		return strings.TrimSpace(sel.Text())
	case "Html":
		h, _ := sel.Html()
		return strings.TrimSpace(h)
	default:
		v, _ := sel.Attr(kind)
		return v
	}
}
