package config

import (
	"context"
	"fmt"
	"testing"
)

func TestSearchableFlags(t *testing.T) {
	for _, tt := range []struct {
		flag    string
		enabled bool
	}{
		{"", true}, {"0", false}, {`"0"`, false}, {"false", false}, {"1", true}, {`"2"`, true}, {"true", true}, {"null", true},
	} {
		t.Run("flag="+tt.flag, func(t *testing.T) {
			field := ""
			if tt.flag != "" {
				field = `,"searchable":` + tt.flag
			}
			doc, err := Read(context.Background(), "https://example.com/config.json", func(context.Context, string) (Response, error) {
				return Response{Status: 200, Body: []byte(fmt.Sprintf(`{"sites":[{"key":"demo","type":1,"api":"/api"%s}]}`, field))}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Sites) != 1 || doc.Sites[0].SearchEnabled() != tt.enabled {
				t.Fatalf("sites=%+v", doc.Sites)
			}
		})
	}
}
