package states

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Values holds canonical field values by key. Enum values are members of the
// field's vocabulary (Unset included); text values are normalised and clamped.
type Values map[string]string

// Problem is one refused local value. A refused value is never pushed, never
// guessed and never replaced by a default (the item's state part is skipped and
// the run exits non-zero).
type Problem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Msg   string `json:"msg"`
}

func (p Problem) Error() string { return fmt.Sprintf("%s=%q: %s", p.Key, p.Value, p.Msg) }

// NormResult is the outcome of Normalize.
type NormResult struct {
	Values   Values
	Problems []Problem
	Clamped  []string // keys whose text was truncated to the field limit
}

// NormalizeText is the canonical form of a text value: CRLF/CR become LF, the
// whole value is trimmed, control characters other than LF/TAB are refused. It
// is idempotent.
func NormalizeText(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("invalid UTF-8")
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (unicode.IsControl(r) && r != '\u00a0') {
			return "", fmt.Errorf("control character U+%04X", r)
		}
	}
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return strings.TrimSpace(s), nil
}

// Normalize turns raw column text into canonical Values. A key missing from raw
// or an empty enum is the field's Unset value / the empty text; an enum token
// outside the closed vocabulary is a Problem and is OMITTED from Values.
func (s *Spec) Normalize(raw map[string]string) NormResult {
	res := NormResult{Values: Values{}}
	for _, f := range s.Fields {
		v := raw[f.Key]
		switch f.Kind {
		case KindEnum:
			t := strings.TrimSpace(v)
			if t == "" {
				res.Values[f.Key] = f.Unset
				continue
			}
			ok := false
			for _, allowed := range f.Values {
				if t == allowed {
					ok = true
					break
				}
			}
			if !ok {
				res.Problems = append(res.Problems, Problem{Key: f.Key, Value: v, Msg: "not in the closed vocabulary"})
				continue
			}
			res.Values[f.Key] = t
		case KindText:
			t, err := NormalizeText(v)
			if err != nil {
				res.Problems = append(res.Problems, Problem{Key: f.Key, Value: truncateForReport(v), Msg: err.Error()})
				continue
			}
			if lim := f.RunesLimit(); utf8.RuneCountInString(t) > lim {
				r := []rune(t)[:lim]
				t = strings.TrimSpace(string(r))
				res.Clamped = append(res.Clamped, f.Key)
			}
			res.Values[f.Key] = t
		}
	}
	return res
}

func truncateForReport(v string) string {
	r := []rune(v)
	if len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return v
}

// IsUnset reports whether value is the "no state" value of key (an empty text,
// or the Unset member of an enum).
func (s *Spec) IsUnset(key, value string) bool {
	f, ok := s.Field(key)
	if !ok {
		return false
	}
	if f.Kind == KindEnum {
		return value == f.Unset
	}
	return value == ""
}
