// Package urljoin resolves a reference against a base URL the way drpy's
// joinUrl does: absolute references win, everything else follows the URL
// standard (net/url ResolveReference).
package urljoin

import (
	"net/url"
	"strings"
)

// Join resolves ref against base. An absolute ref is returned unchanged.
// An empty ref returns base. A base that is not an absolute URL is joined
// by a simple path concatenation so callers still get a usable string.
func Join(base, ref string) string {
	base = strings.TrimSpace(base)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return base
	}
	if base == "" {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if ru.IsAbs() {
		return ru.String()
	}
	bu, err := url.Parse(base)
	if err != nil || bu.Scheme == "" || bu.Host == "" {
		if strings.HasPrefix(ref, "/") {
			return strings.TrimRight(base, "/") + ref
		}
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(ref, "/")
	}
	return bu.ResolveReference(ru).String()
}
