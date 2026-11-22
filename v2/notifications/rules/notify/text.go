package notify

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The Python service counts, trims and splits text as Python's str does; these say the same in Go.

// length is a text's length in characters (code points), as Python's len counts a str.
func length(s string) int { return utf8.RuneCountInString(s) }

// truncated is a text's first n characters.
func truncated(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// isSpace is Python's str.isspace for one character: Unicode's white space and the four separators U+001C to
// U+001F, which Python also strips.
func isSpace(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }

// isLineBreak is a boundary Python's str.splitlines splits at.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// lines are a text's non-empty lines, split as str.splitlines splits them. An empty line is dropped, which the
// one caller would drop anyway as blank.
func lines(s string) []string { return strings.FieldsFunc(s, isLineBreak) }

// asciiJSON escapes every non-ASCII character of a JSON document, and DEL, as \uXXXX, as Python's json.dumps does by
// default, so the outbox's size bound counts the bytes the Python service counted. Outside strings a JSON document
// is printable ASCII, so every such character is inside a string, where the escape means the same.
func asciiJSON(document []byte) []byte {
	var out bytes.Buffer
	for len(document) > 0 {
		r, size := utf8.DecodeRune(document)
		switch {
		case r < utf8.RuneSelf && r != 0x7f:
			out.WriteByte(document[0])
		case r > 0xffff:
			high, low := utf16Pair(r)
			fmt.Fprintf(&out, `\u%04x\u%04x`, high, low)
		default:
			fmt.Fprintf(&out, `\u%04x`, r)
		}
		document = document[size:]
	}
	return out.Bytes()
}

// utf16Pair is the surrogate pair that spells a character beyond the Basic Multilingual Plane.
func utf16Pair(r rune) (rune, rune) {
	r -= 0x10000
	return 0xd800 + r>>10, 0xdc00 + r&0x3ff
}
