package server

import "testing"

func TestResumeOffset(t *testing.T) {
	cases := []struct {
		header string
		want   int
	}{
		// A fresh connection has no header: start from the beginning.
		{"", 0},
		// A reconnect sends the last id the browser received, so the server
		// resumes AFTER it — re-sending it would duplicate a milestone.
		{"0", 1},
		{"5", 6},
		{"42", 43},
		// Malformed or negative values are treated as a fresh connection.
		{"-1", 0},
		{"abc", 0},
		{"3.14", 0},
		{" 10 ", 0},
	}
	for _, tt := range cases {
		got := resumeOffset(tt.header)
		if got != tt.want {
			t.Fatalf("resumeOffset(%q) = %d, want %d", tt.header, got, tt.want)
		}
	}
}
