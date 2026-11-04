package acp

import (
	"strings"
	"unicode/utf8"
)

// StripControls removes terminal controls from agent text while retaining newlines.
func StripControls(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i = skipEscapeSeq(s, i+1)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if stripControlRune(r) {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func stripControlRune(r rune) bool {
	if r == '\n' {
		return false
	}
	return r <= 0x1f || (r >= 0x7f && r <= 0x9f)
}

func skipEscapeSeq(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	switch s[i] {
	case ']', '(', 'P', '^', '_':
		// OSC / DCS / SOS / PM / APC — terminate on BEL or ST (ESC \).
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\x07' {
				return j + 1
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	case '[':
		for j := i + 1; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	default:
		return i + 1
	}
}
