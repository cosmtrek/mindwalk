package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		limit  int
		marker string
		want   string
	}{
		{name: "unchanged", text: "你好", limit: 2, marker: "...", want: "你好"},
		{name: "multibyte boundary", text: "aa界tail", limit: 6, marker: "...", want: "aa界..."},
		{name: "emoji boundary", text: "aa🙂tail", limit: 6, marker: "...", want: "aa🙂..."},
		{name: "marker within budget", text: "abcdef", limit: 4, marker: "…", want: "abc…"},
		{name: "marker fills budget", text: "abcdef", limit: 2, marker: "...", want: ".."},
		{name: "zero budget", text: "abcdef", limit: 0, marker: "…", want: ""},
		{name: "invalid input", text: string([]byte{'o', 'k', 0xff}), limit: 4, want: "ok�"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.text, tt.limit, tt.marker)
			if got != tt.want {
				t.Fatalf("TruncateRunes() = %q, want %q", got, tt.want)
			}

			if !utf8.ValidString(got) {
				t.Fatalf("TruncateRunes() returned invalid UTF-8: %q", got)
			}

			if gotRunes := len([]rune(got)); gotRunes > tt.limit {
				t.Fatalf("TruncateRunes() returned %d runes, limit %d", gotRunes, tt.limit)
			}
		})
	}
}
