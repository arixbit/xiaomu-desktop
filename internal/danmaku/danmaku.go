// Package danmaku reads timed comments without providing a comment service.
package danmaku

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxComments = 50000

// Comment is one scrolling (1), bottom (4), or top (5) comment.
type Comment struct {
	Time  time.Duration
	Mode  int
	Color uint32
	Text  string
}

// Parse accepts Dandanplay JSON or the standard Bilibili XML format.
// Unsupported animation modes and malformed individual comments are skipped.
func Parse(data []byte) ([]Comment, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	data = bytes.TrimSpace(data)
	var comments []Comment
	if bytes.HasPrefix(data, []byte("{")) {
		var response struct {
			ErrorCode int `json:"errorCode"`
			Comments  *[]struct {
				Params string `json:"p"`
				Text   string `json:"m"`
			} `json:"comments"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("解析弹幕 JSON：%w", err)
		}
		if response.ErrorCode != 0 || response.Comments == nil {
			return nil, fmt.Errorf("弹幕服务没有返回有效的弹幕列表")
		}
		comments = make([]Comment, 0, min(len(*response.Comments), maxComments))
		for _, raw := range *response.Comments {
			if comment, ok := parseComment(raw.Params, raw.Text, false); ok {
				comments = append(comments, comment)
			}
			if len(comments) == maxComments {
				break
			}
		}
	} else {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		root := false
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("解析弹幕 XML：%w", err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			if !root {
				if start.Name.Local != "i" {
					return nil, fmt.Errorf("响应不是弹幕 JSON 或 XML")
				}
				root = true
				continue
			}
			if start.Name.Local != "d" {
				continue
			}
			var raw struct {
				Params string `xml:"p,attr"`
				Text   string `xml:",chardata"`
			}
			if err := decoder.DecodeElement(&raw, &start); err != nil {
				return nil, fmt.Errorf("解析弹幕内容：%w", err)
			}
			if comment, ok := parseComment(raw.Params, raw.Text, true); ok {
				comments = append(comments, comment)
			}
			if len(comments) == maxComments {
				break
			}
		}
		if !root {
			return nil, fmt.Errorf("响应不是弹幕 JSON 或 XML")
		}
	}
	slices.SortStableFunc(comments, func(a, b Comment) int {
		return cmp.Compare(a.Time, b.Time)
	})
	return comments, nil
}

func parseComment(params, text string, xmlFormat bool) (Comment, bool) {
	f0, rest, ok := strings.Cut(params, ",")
	if !ok {
		return Comment{}, false
	}
	f1, rest, ok := strings.Cut(rest, ",")
	if !ok {
		return Comment{}, false
	}
	var colorField string
	if xmlFormat {
		_, rest, ok = strings.Cut(rest, ",")
		if !ok {
			return Comment{}, false
		}
		colorField, _, _ = strings.Cut(rest, ",")
	} else {
		colorField, _, _ = strings.Cut(rest, ",")
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(f0), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 24*60*60 {
		return Comment{}, false
	}
	mode, err := strconv.Atoi(strings.TrimSpace(f1))
	if err != nil || mode != 1 && mode != 4 && mode != 5 {
		return Comment{}, false
	}
	color, err := strconv.ParseUint(strings.TrimSpace(colorField), 10, 24)
	if err != nil {
		return Comment{}, false
	}
	text = cleanCommentText(text)
	if text == "" {
		return Comment{}, false
	}
	return Comment{Time: time.Duration(seconds * float64(time.Second)), Mode: mode, Color: uint32(color), Text: text}, true
}

func cleanCommentText(text string) string {
	if text == "" {
		return ""
	}
	// Fast path: already normalized (no leading/trailing/repeated/non-space
	// whitespace) and within the 200-rune cap avoids all allocations.
	clean := true
	runeCount := 0
	prevSpace := true
	for i := 0; i < len(text); {
		b := text[i]
		if b < utf8.RuneSelf {
			if b == ' ' {
				if prevSpace {
					clean = false
					break
				}
				prevSpace = true
			} else if b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f' {
				clean = false
				break
			} else {
				prevSpace = false
			}
			i++
		} else {
			r, size := utf8.DecodeRuneInString(text[i:])
			if unicode.IsSpace(r) {
				clean = false
				break
			}
			prevSpace = false
			i += size
		}
		runeCount++
		if runeCount > 200 {
			clean = false
			break
		}
	}
	if clean && !prevSpace {
		return text
	}
	var b strings.Builder
	b.Grow(min(len(text), 200*3))
	written := 0
	inSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if written > 0 {
				inSpace = true
			}
			continue
		}
		if inSpace {
			if written >= 200 {
				break
			}
			b.WriteByte(' ')
			written++
			inSpace = false
		}
		if written >= 200 {
			break
		}
		b.WriteRune(r)
		written++
	}
	return b.String()
}
