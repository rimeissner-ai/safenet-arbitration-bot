package cli

import "testing"

func TestPrintable(t *testing.T) {
	for _, test := range []struct {
		text, want string
	}{
		{"contract C {\n\tuint x; // ünïcode\n}", "contract C {\n\tuint x; // ünïcode\n}"},
		// Escape sequences, carriage returns, and bidirectional text controls, which
		// can hide what source code says, are escaped.
		{"a\x1b[2Jb\rc", `a\x1b[2Jb\rc`},
		{"if (admin\u202e) {", `if (admin\u202e) {`},
		{"\x00", `\x00`},
	} {
		if got := printable(test.text); got != test.want {
			t.Errorf("printable(%q) = %q, want %q", test.text, got, test.want)
		}
	}
}
