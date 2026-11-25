package sieve

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type compiledRegex = regexp.Regexp

// match tells whether any value matches any key (for :count: whether the
// number of values matches any key) and sets the match variables on a
// successful :matches or :regex.
func (r *runner) match(sc *scope, m matcher, values, keys []string) (bool, error) {
	expanded := make([]string, len(keys))
	for i, k := range keys {
		expanded[i] = r.expand(sc, k)
	}
	if m.kind == "count" {
		n := strconv.Itoa(len(values))
		for _, k := range expanded {
			if err := r.step(1); err != nil {
				return false, err
			}
			if relational(compare(m.comparator, n, k), m.rel) {
				return true, nil
			}
		}
		return false, nil
	}
	for _, v := range values {
		for _, k := range expanded {
			// Long values cost more.
			if err := r.step(1 + len(v)/4096); err != nil {
				return false, err
			}
			ok, caps, err := r.matchOne(sc, m, v, k)
			if err != nil {
				return false, err
			}
			if ok {
				if caps != nil && sc.script.exts["variables"] {
					r.matchVars = caps
				}
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *runner) matchOne(sc *scope, m matcher, v, k string) (bool, []string, error) {
	switch m.kind {
	case "is":
		return compare(m.comparator, v, k) == 0, nil, nil
	case "contains":
		if m.comparator == "i;ascii-casemap" {
			return strings.Contains(asciiLower(v), asciiLower(k)), nil, nil
		}
		return strings.Contains(v, k), nil, nil
	case "value":
		return relational(compare(m.comparator, v, k), m.rel), nil, nil
	case "matches":
		caps, ok := wildcard(v, k, m.comparator == "i;ascii-casemap")
		return ok, caps, nil
	case "regex":
		re, err := r.regex(k, m.comparator == "i;ascii-casemap")
		if err != nil {
			return false, nil, err
		}
		sub := re.FindStringSubmatch(v)
		if sub == nil {
			return false, nil, nil
		}
		if len(sub) > 10 {
			sub = sub[:10]
		}
		return true, sub, nil
	}
	return false, nil, nil
}

func (r *runner) regex(pattern string, fold bool) (*regexp.Regexp, error) {
	if len(pattern) > MaxRegexBytes {
		return nil, &Error{Msg: "regular expression too long"}
	}
	key := pattern
	if fold {
		key = "(?i)" + pattern
	}
	if c, ok := r.regexes[key]; ok {
		return c.re, c.err
	}
	if len(r.regexes) > 64 {
		return nil, &Error{Msg: "too many different regular expressions"}
	}
	re, err := regexp.Compile(key)
	if err != nil {
		err = &Error{Msg: "regular expression " + strconv.Quote(pattern) + " is not valid"}
	}
	r.regexes[key] = &regexCache{re: re, err: err}
	return re, err
}

func relational(c int, op string) bool {
	switch op {
	case "gt":
		return c > 0
	case "ge":
		return c >= 0
	case "lt":
		return c < 0
	case "le":
		return c <= 0
	case "eq":
		return c == 0
	case "ne":
		return c != 0
	}
	return false
}

// compare orders a and b under a comparator.
func compare(comparator, a, b string) int {
	switch comparator {
	case "i;ascii-casemap":
		return strings.Compare(asciiLower(a), asciiLower(b))
	case "i;ascii-numeric":
		return compareNumeric(a, b)
	}
	return strings.Compare(a, b)
}

// compareNumeric compares the leading digits as numbers; a string without
// leading digits is positive infinity (RFC 4790 9.1).
func compareNumeric(a, b string) int {
	na, oka := leadingNumber(a)
	nb, okb := leadingNumber(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return 1
	case !okb:
		return -1
	}
	return na.Cmp(nb)
}

func leadingNumber(s string) (*big.Int, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return nil, false
	}
	n, _ := new(big.Int).SetString(s[:i], 10)
	return n, true
}

// wildcardBudget bounds the work of one wildcard match; a match that
// needs more counts as no match.
const wildcardBudget = 1_000_000

// wildcard matches v against a pattern with * (any sequence, as short as
// possible) and ? (one character); \ escapes. It returns ${0} … ${9}:
// the whole value and what each wildcard matched.
func wildcard(v, pattern string, fold bool) ([]string, bool) {
	w, p := v, pattern
	if fold {
		// Only ASCII letters fold, so byte offsets stay the same.
		w, p = asciiLower(v), asciiLower(pattern)
	}
	caps, ok := wildcardOffsets(w, p)
	if !ok {
		return nil, false
	}
	out := []string{v}
	for _, c := range caps {
		if len(out) == 10 {
			break
		}
		out = append(out, v[c.start:c.end])
	}
	return out, true
}

type wtok struct {
	lit  string // literal text
	kind byte   // 0 literal, '*', '?'
}

func wildcardTokens(p string) []wtok {
	var out []wtok
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, wtok{lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '\\':
			if i+1 < len(p) {
				i++
				lit.WriteByte(p[i])
			}
		case '*', '?':
			flush()
			out = append(out, wtok{kind: c})
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return out
}

// capture is one wildcard's match as byte offsets in the value.
type capture struct{ start, end int }

func wildcardOffsets(v, pattern string) ([]capture, bool) {
	toks := wildcardTokens(pattern)
	var caps []capture
	budget := wildcardBudget
	// Backtracking with a memo of failed (token, offset) pairs.
	failed := map[[2]int]bool{}
	var walk func(ti, off int) bool
	walk = func(ti, off int) bool {
		budget--
		if budget < 0 {
			return false
		}
		if ti == len(toks) {
			return off == len(v)
		}
		key := [2]int{ti, off}
		if failed[key] {
			return false
		}
		t := toks[ti]
		switch t.kind {
		case 0:
			if strings.HasPrefix(v[off:], t.lit) && walk(ti+1, off+len(t.lit)) {
				return true
			}
		case '?':
			if off < len(v) {
				_, size := utf8.DecodeRuneInString(v[off:])
				caps = append(caps, capture{off, off + size})
				if walk(ti+1, off+size) {
					return true
				}
				caps = caps[:len(caps)-1]
			}
		case '*':
			for end := off; end <= len(v) && budget > 0; {
				caps = append(caps, capture{off, end})
				if walk(ti+1, end) {
					return true
				}
				caps = caps[:len(caps)-1]
				if end == len(v) {
					break
				}
				_, size := utf8.DecodeRuneInString(v[end:])
				end += size
			}
		}
		if budget > 0 {
			failed[key] = true
		}
		return false
	}
	if !walk(0, 0) {
		return nil, false
	}
	return caps, true
}

// quoteMeta escapes regular expression characters (:quoteregex).
func quoteMeta(s string) string { return regexp.QuoteMeta(s) }
