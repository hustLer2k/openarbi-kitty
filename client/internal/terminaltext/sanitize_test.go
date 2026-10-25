package terminaltext

import (
	"testing"
)

func TestSanitizeTerminalLanguages(t *testing.T) {
	cases := map[string]string{
		"plain\ntext\tOK":                                 "plain\ntext    OK",
		"a\x1b[31mred\x1b[0mz":                            "aredz",
		"a\x1b]52;c;c2VjcmV0\ab":                          "ab",
		"a\x1b]8;;https://evil\x1b\\click\x1b]8;;\x1b\\b": "aclickb",
		"a\x1bPoperate\x1b\\b":                            "ab",
		"a\x1b_kitty-graphics\x1b\\b":                     "ab",
		"a\x1b^private\x1b\\b":                            "ab",
		"a\x9b31mb":                                       "ab",
		"a\u009dclipboard\u009cb":                         "ab",
		"a\r\b\x00\x7fb":                                  "ab",
		"a\u202eevil\u202cb":                              "aevilb",
		"a\x1b]unterminated":                              "a",
	}
	for input, want := range cases {
		if got := Sanitize(input); got != want {
			t.Errorf("%q => %q want %q", input, got, want)
		}
	}
}
func FuzzSanitizeNeverEmitsControls(f *testing.F) {
	f.Add("hello\x1b[31mworld\x1b[0m")
	f.Fuzz(func(t *testing.T, input string) {
		for _, r := range Sanitize(input) {
			if (r < 32 && r != '\n') || (r >= 127 && r <= 159) {
				t.Fatalf("control U+%04X survived", r)
			}
		}
	})
}
