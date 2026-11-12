package config

import "bytes"

// Strip turns JSON with //, /* */, # comments and trailing commas into JSON
// that encoding/json accepts. Comment markers inside strings stay untouched.
func Strip(in []byte) []byte {
	in = bytes.TrimPrefix(in, []byte{0xEF, 0xBB, 0xBF})
	var b bytes.Buffer
	b.Grow(len(in))
	inStr := false
	esc := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == '#' || c == '/' && i+1 < len(in) && in[i+1] == '/' {
			for i < len(in) && in[i] != '\n' && in[i] != '\r' {
				i++
			}
			if i < len(in) {
				b.WriteByte(in[i])
			}
			continue
		}
		if c == '/' && i+1 < len(in) && in[i+1] == '*' {
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			if i+1 < len(in) {
				i++ // skip '/'
			}
			continue
		}
		if c == ',' && closesAfter(in, i+1) {
			continue
		}
		b.WriteByte(c)
	}
	return b.Bytes()
}

// closesAfter reports whether the next non-space, non-comment token is
// a closing brace or bracket. That comma is trailing.
func closesAfter(in []byte, i int) bool {
	next := skipSpaceAndComments(in[i:])
	return len(next) > 0 && (next[0] == '}' || next[0] == ']')
}
