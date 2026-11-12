package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"unicode"
)

func isImage(b []byte) bool {
	if len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return true
	}
	sig := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	return len(b) >= len(sig) && bytes.Equal(b[:len(sig)], sig)
}

// Decode unwraps a config body. Plain JSON (including comments) is returned
// as-is. A JPEG or PNG, or any payload that is not itself JSON, may carry
// prefix**base64 after the bytes; the prefix is ignored and the base64 is
// decoded. The first "**" whose tail decodes to JSON wins, so a compressed
// image that happens to contain the two bytes is skipped.
func Decode(body []byte) ([]byte, error) {
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	if isImage(body) {
		if dec, ok := decodeMarked(body); ok {
			return dec, nil
		}
		return nil, errImage
	}
	trim := bytes.TrimSpace(body)
	if looksLikeJSON(trim) {
		return trim, nil
	}
	if dec, ok := decodeMarked(body); ok {
		return dec, nil
	}
	return trim, nil
}

var errImage = errString("config: image has no prefix**base64 JSON payload")

type errString string

func (e errString) Error() string { return string(e) }

func looksLikeJSON(b []byte) bool {
	s := skipSpaceAndComments(b)
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

func skipSpaceAndComments(b []byte) []byte {
	for {
		b = bytes.TrimLeftFunc(b, unicode.IsSpace)
		if bytes.HasPrefix(b, []byte("//")) || bytes.HasPrefix(b, []byte("#")) {
			if i := bytes.IndexAny(b, "\r\n"); i >= 0 {
				b = b[i+1:]
				continue
			}
			return nil
		}
		if bytes.HasPrefix(b, []byte("/*")) {
			if i := bytes.Index(b, []byte("*/")); i >= 0 {
				b = b[i+2:]
				continue
			}
			return nil
		}
		return b
	}
}

func decodeMarked(body []byte) ([]byte, bool) {
	rest := body
	for {
		i := bytes.Index(rest, []byte("**"))
		if i < 0 {
			return nil, false
		}
		if dec, ok := tryB64JSON(rest[i+2:]); ok {
			return dec, true
		}
		rest = rest[i+2:]
	}
}

func tryB64JSON(b []byte) ([]byte, bool) {
	s := takeBase64(b)
	if len(s) < 8 {
		return nil, false
	}
	dec, err := b64decode(s)
	if err != nil {
		return nil, false
	}
	dec = bytes.TrimSpace(dec)
	if !looksLikeJSON(dec) {
		return nil, false
	}
	return dec, true
}

func takeBase64(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == ' ' || c == '\n' || c == '\r' || c == '\t':
			continue
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '-' || c == '_':
			sb.WriteByte(c)
		default:
			return sb.String()
		}
	}
	return sb.String()
}

func b64decode(s string) ([]byte, error) {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
