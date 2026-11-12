package config

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// UnmarshalJSON accepts the numeric and quoted numeric type values used by
// TVBox configurations, while keeping Type an int for playback dispatch.
func (p *Parser) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name string          `json:"name"`
		Type json.Number     `json:"type"`
		URL  string          `json:"url"`
		Ext  json.RawMessage `json:"ext"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	typ := 0
	if raw.Type != "" {
		n, err := strconv.Atoi(raw.Type.String())
		if err != nil {
			return fmt.Errorf("config: parser %q type must be an integer: %w", raw.Name, err)
		}
		typ = n
	}
	*p = Parser{Name: raw.Name, Type: typ, URL: raw.URL, Ext: raw.Ext}
	return nil
}
