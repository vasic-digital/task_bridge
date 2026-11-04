package states

import (
	"flag"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func mustSpec(t testing.TB) *Spec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "spec_repro.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSpec(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// golden compares got with testdata/<name>.golden (or rewrites it under -update).
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("golden %s: %v (run go test -update)", p, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs:\n--- want\n%s\n--- got\n%s", p, want, got)
	}
}

// alphabet used by the property tests: the characters that stress the encoding.
var propAlphabet = []rune("ab Z09\\\\nt*_[]():-.#|`~<>!\"'\n\t:;/éж漢,{}")

func randString(r *rand.Rand, max int) string {
	n := r.Intn(max + 1)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteRune(propAlphabet[r.Intn(len(propAlphabet))])
	}
	return b.String()
}

// mdChannel simulates a Markdown processor that re-serialises the source: it
// backslash-escapes ordinary ASCII punctuation but leaves our own escape tokens
// (\\ \n \t) alone, because a Markdown parser reads "\\" as one literal
// backslash and re-emits it as "\\".
func mdChannel(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) && (rs[i+1] == '\\' || rs[i+1] == 'n' || rs[i+1] == 't') {
			b.WriteRune(r)
			b.WriteRune(rs[i+1])
			i++
			continue
		}
		if r < 0x80 && strings.ContainsRune("*_[]()#+!|~<>`", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sloppyChannel simulates CRLF line ends, trailing blanks and NBSP spaces.
func sloppyChannel(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(l, " ", "\u00a0") + "  "
	}
	return strings.Join(lines, "\r\n")
}
