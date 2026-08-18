package tools

import "testing"

func TestStripANSI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"\x1b[38;2;52;52;52mgradient\x1b[0m", "gradient"},
		{"\x1b[1mbold\x1b[0m and \x1b[31mred\x1b[0m", "bold and red"},
		{"\x1b]0;title\x07body", "body"},
		{"line\x1b[2K\x1b[1Gerased", "lineerased"},
	}
	for _, tt := range tests {
		if got := stripANSI(tt.in); got != tt.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
