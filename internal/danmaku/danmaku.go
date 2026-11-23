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
	fields := strings.Split(params, ",")
	colorIndex := 2
	if xmlFormat {
		colorIndex = 3
	}
	if len(fields) <= colorIndex {
		return Comment{}, false
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 24*60*60 {
		return Comment{}, false
	}
	mode, err := strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil || mode != 1 && mode != 4 && mode != 5 {
		return Comment{}, false
	}
	color, err := strconv.ParseUint(strings.TrimSpace(fields[colorIndex]), 10, 24)
	if err != nil {
		return Comment{}, false
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 200 {
		text = string(runes[:200])
	}
	if text == "" {
		return Comment{}, false
	}
	return Comment{Time: time.Duration(seconds * float64(time.Second)), Mode: mode, Color: uint32(color), Text: text}, true
}
