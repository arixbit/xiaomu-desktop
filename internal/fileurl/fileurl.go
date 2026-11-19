// Package fileurl converts file:// URLs to local paths.
//
// Both the config loader and the drpy module loader read local files
// through file:// URLs, and both used to strip the "file://" prefix by
// hand: file:///C:/x became /C:/x on Windows, and %20 or %E4%B8%AD in a
// name stayed encoded. Parsing the URL instead decodes the percent
// escapes and keeps drive letters intact.
package fileurl

import (
	"fmt"
	"net/url"
	"runtime"
	"strings"
)

// Path converts raw, a file:// URL, to a local path for this OS.
func Path(raw string) (string, error) {
	return PathFor(raw, runtime.GOOS == "windows")
}

// PathFor is Path with the OS chosen by the caller, so tests can cover
// Windows paths without Windows.
func PathFor(raw string, windows bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("fileurl: %q: %w", raw, err)
	}
	if u.Scheme != "file" || u.Opaque != "" {
		return "", fmt.Errorf("fileurl: %q is not a file:// URL", raw)
	}
	// u.Path is already percent-decoded by url.Parse.
	p := u.Path
	host := u.Hostname()
	if windows {
		if host != "" && !strings.EqualFold(host, "localhost") {
			// file://server/share/path is a UNC path.
			return `\\` + host + strings.ReplaceAll(p, "/", "\\"), nil
		}
		// A drive letter keeps its leading slash in the URL:
		// file:///C:/x -> /C:/x -> C:\x.
		if len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':' {
			p = p[1:]
		}
		return strings.ReplaceAll(p, "/", "\\"), nil
	}
	if host != "" && !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("fileurl: %q: unexpected host %q", raw, host)
	}
	return p, nil
}

func isDriveLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
