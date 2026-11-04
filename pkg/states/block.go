package states

import (
	"strings"
)

// BlockVersion is written into the BEGIN marker line.
const BlockVersion = 1

// blockNote is the human note on the BEGIN line. It is informational: the
// parser keys on the marker prefix only.
const blockNote = "managed by task_bridge from the tracker database; edit it there, not here"

// asciiPunct is the set of characters a Markdown processor may backslash-escape.
const asciiPunct = "!\"#$%&'()*+,-./:;<=>?@[]^_`{|}~"

// encodeText renders a text value on ONE line: backslash, LF and TAB become the
// two-character escapes \\ \n \t. Nothing else is touched, so the encoder never
// emits a backslash directly before an ASCII punctuation character except as the
// first half of "\\" (which decodeText consumes first). That property is what
// lets decodeText tolerate a Markdown channel that escapes punctuation.
func encodeText(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// decodeText is the tolerant inverse of encodeText. Besides \\ \n \t it drops a
// backslash that precedes an ASCII punctuation character (the escape a Markdown
// round trip may add); any other backslash is kept literally.
func decodeText(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r != '\\' || i+1 >= len(rs) {
			b.WriteRune(r)
			continue
		}
		n := rs[i+1]
		switch {
		case n == '\\':
			b.WriteRune('\\')
			i++
		case n == 'n':
			b.WriteRune('\n')
			i++
		case n == 't':
			b.WriteRune('\t')
			i++
		case n < 0x80 && strings.ContainsRune(asciiPunct, n):
			b.WriteRune(n)
			i++
		default:
			b.WriteRune('\\')
		}
	}
	return b.String()
}

func (s *Spec) beginLine() string {
	return "[" + s.ID() + ":BEGIN v" + itoa(BlockVersion) + "] " + blockNote
}

func (s *Spec) endLine() string { return "[" + s.ID() + ":END]" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// RenderBlock renders the managed block carrying keys (in spec order). An
// Unset enum or an empty text gets no line unless materializeUnset. When no
// line remains the result is "" (no block at all).
func (s *Spec) RenderBlock(keys []string, vals Values, materializeUnset bool) string {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var lines []string
	for _, f := range s.Fields {
		if !want[f.Key] {
			continue
		}
		v, ok := vals[f.Key]
		if !ok {
			if f.Kind == KindEnum {
				v = f.Unset
			}
		}
		if s.IsUnset(f.Key, v) && !materializeUnset {
			continue
		}
		if f.Kind == KindEnum {
			lines = append(lines, f.Key+": "+v)
		} else {
			lines = append(lines, strings.TrimRight(f.Key+": "+encodeText(v), " "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	out := append([]string{s.beginLine()}, lines...)
	out = append(out, s.endLine())
	return strings.Join(out, "\n")
}

// ParsedBlock is the tolerant parse of a task description.
type ParsedBlock struct {
	// Count is the number of well-formed BEGIN..END regions found.
	Count int
	// Malformed is true on a BEGIN with no END, a nested BEGIN, or a stray END:
	// the region boundaries are ambiguous, so the description must not be rewritten.
	Malformed bool
	// Values holds the decoded value of every known key found in the FIRST region.
	Values map[string]string
	// Unknown lists keys in a region that the Spec does not define.
	Unknown []string
	// Foreign counts region lines that are not "key: value".
	Foreign int
	// Base is the description text outside every region, right-trimmed.
	Base string
}

func normalizeDesc(desc string) []string {
	d := strings.ReplaceAll(desc, "\r\n", "\n")
	d = strings.ReplaceAll(d, "\r", "\n")
	d = strings.ReplaceAll(d, "\u00a0", " ")
	return strings.Split(d, "\n")
}

// marker reports whether line is a BEGIN or END marker line. Backslashes are
// ignored for the match so a Markdown-escaped marker ("\[TB\-STATES:BEGIN") is
// still recognised.
func (s *Spec) marker(line string) (begin, end bool) {
	loose := strings.ReplaceAll(strings.TrimSpace(line), `\`, "")
	if strings.HasPrefix(loose, "["+s.ID()+":BEGIN") {
		return true, false
	}
	if strings.HasPrefix(loose, "["+s.ID()+":END") {
		return false, true
	}
	return false, false
}

// ParseBlock extracts the managed block(s) and the surrounding base text.
func (s *Spec) ParseBlock(desc string) ParsedBlock {
	pb := ParsedBlock{Values: map[string]string{}}
	lines := normalizeDesc(desc)
	var base []string
	inside := false
	first := true
	known := map[string]bool{}
	for _, k := range s.Keys() {
		known[k] = true
	}
	for _, ln := range lines {
		b, e := s.marker(ln)
		switch {
		case b && inside:
			pb.Malformed = true
			base = append(base, ln)
		case b:
			inside = true
		case e && !inside:
			pb.Malformed = true
			base = append(base, ln)
		case e:
			inside = false
			pb.Count++
			first = false
		case inside:
			if strings.TrimSpace(ln) == "" {
				continue
			}
			key, val, ok := splitKV(ln)
			if !ok {
				pb.Foreign++
				continue
			}
			if !known[key] {
				pb.Unknown = append(pb.Unknown, key)
				continue
			}
			if first {
				if _, dup := pb.Values[key]; !dup {
					pb.Values[key] = val
				}
			}
		default:
			base = append(base, ln)
		}
	}
	if inside {
		pb.Malformed = true
	}
	pb.Base = strings.TrimRight(strings.Join(base, "\n"), " \t\r\n")
	if pb.Malformed {
		// A malformed description is returned untouched as Base.
		pb.Base = strings.TrimRight(strings.Join(lines, "\n"), " \t\r\n")
	}
	return pb
}

func splitKV(line string) (key, val string, ok bool) {
	t := strings.TrimRight(line, " \t")
	i := strings.IndexByte(t, ':')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(strings.ReplaceAll(t[:i], `\`, ""))
	if !keyRe.MatchString(key) {
		return "", "", false
	}
	rest := t[i+1:]
	rest = strings.TrimPrefix(rest, " ")
	return key, decodeText(rest), true
}

// Compose joins a base description and a rendered block: the base is
// right-trimmed, a blank line separates them, and an empty block yields the base
// alone. Compose(ParseBlock(d).Base, b) is the canonical rewrite of d.
func Compose(base, block string) string {
	b := strings.TrimRight(base, " \t\r\n")
	if block == "" {
		return b
	}
	if b == "" {
		return block
	}
	return b + "\n\n" + block
}
