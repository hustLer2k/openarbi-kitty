package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize removes terminal control languages (CSI, OSC, DCS, APC, PM, SOS),
// C0/C1 controls and bidi overrides. Remote text is always data, never terminal
// instructions. Newlines and printable Unicode remain readable.
func Sanitize(text string) string {
	var out strings.Builder
	state := byte(0) // 0 text, 1 ESC, 2 CSI, 3 string, 4 string ESC
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r == utf8.RuneError && size == 1 {
			r = rune(text[0])
			if r < 0x80 || r > 0x9f {
				text = text[size:]
				continue
			}
		}
		text = text[size:]
		switch state {
		case 1:
			switch r {
			case '[':
				state = 2
			case ']', 'P', '_', '^', 'X':
				state = 3
			default:
				if r >= 0x30 && r <= 0x7e {
					state = 0
				}
			}
		case 2:
			if r >= 0x40 && r <= 0x7e {
				state = 0
			}
		case 3:
			if r == 7 || r == 0x9c {
				state = 0
			} else if r == 0x1b {
				state = 4
			}
		case 4:
			if r == '\\' {
				state = 0
			} else {
				state = 3
			}
		default:
			switch r {
			case 0x1b:
				state = 1
			case 0x9b:
				state = 2
			case 0x90, 0x9d, 0x9e, 0x9f, 0x98:
				state = 3
			case '\n':
				out.WriteRune(r)
			case '\t':
				out.WriteString("    ")
			default:
				if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
					out.WriteRune(r)
				}
			}
		}
	}
	return out.String()
}
