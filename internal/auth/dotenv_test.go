package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuoteEnvValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"has space", `"has space"`},
		{"has\ttab", "\"has\ttab\""},
		{`has"quote`, `"has\"quote"`},
		{`back\slash`, `"back\\slash"`},
		{"dollar$var", `"dollar\$var"`},
		{"back`tick", `"back\` + "`" + `tick"`},
		{"bang!val", `"bang\!val"`},
		{"has#comment", `"has#comment"`},
		{"has=equals", `"has=equals"`},
		{"", ""},
		{"noquote", "noquote"},
		{`mix$"val`, `"mix\$\"val"`},
	}
	for _, tt := range tests {
		got := quoteEnvValue(tt.input)
		if got != tt.want {
			t.Errorf("quoteEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestUnquoteEnvValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{`"has space"`, "has space"},
		{`"has\"quote"`, `has"quote`},
		{`"back\\slash"`, `back\slash`},
		{`"dollar\$var"`, "dollar$var"},
		{`"back\` + "`" + `tick"`, "back`tick"},
		{`"bang\!val"`, "bang!val"},
		{`""`, ""},
		{`"a"`, "a"},
	}
	for _, tt := range tests {
		got := UnquoteEnvValue(tt.input)
		if got != tt.want {
			t.Errorf("UnquoteEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuoteEnvValueRoundTripsNewlines(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"newline\nval", `"newline\nval"`},
		{"cr\rval", `"cr\rval"`},
		{"crlf\r\nval", `"crlf\r\nval"`},
	}
	for _, tt := range tests {
		got := quoteEnvValue(tt.input)
		if got != tt.want {
			t.Errorf("quoteEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestUnquoteEnvValueRoundTripsNewlines(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"newline\nval"`, "newline\nval"},
		{`"cr\rval"`, "cr\rval"},
		{`"crlf\r\nval"`, "crlf\r\nval"},
	}
	for _, tt := range tests {
		got := UnquoteEnvValue(tt.input)
		if got != tt.want {
			t.Errorf("UnquoteEnvValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	// All values must survive UpsertEnvFile → ReadEnvValue unchanged.
	values := []string{
		"simple",
		"has space",
		`has"quote`,
		`back\slash`,
		"dollar$var",
		"back`tick",
		"bang!val",
		"token$foo`bar\"baz",
		"has#comment",
		"has=equals",
		"has'single",
		"newline\nembedded",
		"crlf\r\nembedded",
		"",
	}

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	for _, val := range values {
		updates := map[string]string{"KEY": val}
		if err := UpsertEnvFile(path, updates); err != nil {
			t.Fatalf("UpsertEnvFile(%q): %v", val, err)
		}
		got := ReadEnvValue(path, "KEY")
		if got != val {
			t.Errorf("round-trip failed for %q: got %q", val, got)
		}
		_ = os.Remove(path)
	}
}

func TestUpsertEnvFilePreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	_ = os.WriteFile(path, []byte("A=1\nB=2\n"), 0600)

	if err := UpsertEnvFile(path, map[string]string{"B": "updated"}); err != nil {
		t.Fatal(err)
	}

	if got := ReadEnvValue(path, "A"); got != "1" {
		t.Errorf("A = %q, want %q", got, "1")
	}
	if got := ReadEnvValue(path, "B"); got != "updated" {
		t.Errorf("B = %q, want %q", got, "updated")
	}
}
