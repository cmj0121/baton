package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSubmitSequence is the closed set: the three Enter spellings pass through,
// unset is "", and anything else — the backslash-and-r a single-quoted YAML
// '\r' produces above all — is refused with the accepted values and the quoting
// hint, so the user learns the fix from the warning alone.
func TestSubmitSequence(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		bad            bool
	}{
		{name: "unset", in: "", want: ""},
		{name: "carriage return", in: "\r", want: "\r"},
		{name: "line feed", in: "\n", want: "\n"},
		{name: "crlf", in: "\r\n", want: "\r\n"},
		{name: "backslash r", in: `\r`, bad: true},
		{name: "letter", in: "x", bad: true},
		{name: "lf cr", in: "\n\r", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AgentProfile{Command: "grok", Submit: tc.in}.SubmitSequence()
			if tc.bad {
				if err == nil || got != "" {
					t.Fatalf("SubmitSequence(%q) = %q, %v; want an error and nothing", tc.in, got, err)
				}
				for _, want := range []string{`"\r", "\n", "\r\n"`, "double-quote"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q should name %s", err, want)
					}
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("SubmitSequence(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// TestSubmitFromYAML covers the quoting the field's comment warns about: a
// double-quoted "\r" is the control byte, a single-quoted '\r' is two
// characters and is refused, and an empty string is the same as no key.
func TestSubmitFromYAML(t *testing.T) {
	var c Config
	src := `
panel:
  agents:
    grok:
      command: grok
      submit: "\r"
    single:
      command: grok
      submit: '\r'
    empty:
      command: claude
      submit: ""
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, err := c.Panel.Agents["grok"].SubmitSequence(); got != "\r" || err != nil {
		t.Fatalf(`double-quoted "\r" = %q, %v; want CR`, got, err)
	}
	if _, err := c.Panel.Agents["single"].SubmitSequence(); err == nil {
		t.Fatalf(`single-quoted '\r' should be refused, it is a backslash and an r`)
	}
	if got, err := c.Panel.Agents["empty"].SubmitSequence(); got != "" || err != nil {
		t.Fatalf(`submit: "" = %q, %v; want unset`, got, err)
	}
}
