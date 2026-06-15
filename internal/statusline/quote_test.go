package statusline

import "testing"

// quoteIfNeeded must quote on cmd.exe / POSIX shell metacharacters, not just
// spaces — otherwise an exe path like C:\Users\R&D\...\auxly.exe is split by
// cmd.exe at the '&' and the configured statusline never runs on Windows.
func TestQuoteIfNeeded(t *testing.T) {
	bare := []string{
		`auxly`,
		`/usr/local/bin/auxly`,
		`C:\Users\John\AppData\Local\Programs\auxly\auxly.exe`, // clean default path stays bare
	}
	for _, s := range bare {
		if got := quoteIfNeeded(s); got != s {
			t.Errorf("clean path must stay bare: quoteIfNeeded(%q) = %q", s, got)
		}
	}

	quoted := []string{
		`/opt/my apps/auxly`,                              // space (old trigger, still works)
		`C:\Users\R&D\auxly.exe`,                          // cmd.exe '&'
		`C:\Users\a(b)\auxly.exe`,                         // parens
		`C:\Users\x^y\auxly.exe`,                          // caret
		`C:\Users\100%\auxly.exe`,                         // percent
	}
	for _, s := range quoted {
		got := quoteIfNeeded(s)
		if got != `"`+s+`"` {
			t.Errorf("metachar path must be double-quoted: quoteIfNeeded(%q) = %q", s, got)
		}
	}
}
