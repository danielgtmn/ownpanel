package store

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// ErrNotFound marks a missing site, user or token.
	ErrNotFound = errors.New("not found")
	// ErrInvalid marks errors caused by the caller's input (bad name or
	// domain, domain already used), as opposed to storage failures.
	ErrInvalid = errors.New("invalid input")
	// ErrConflict marks requests that are fine in themselves but clash with
	// the current data (name taken, last admin, user still owns sites).
	ErrConflict = errors.New("conflict")

	nameRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// kindError keeps a human-readable message unchanged while still matching
// one of the sentinel errors above.
type kindError struct {
	kind error
	msg  string
}

func (e *kindError) Error() string        { return e.msg }
func (e *kindError) Is(target error) bool { return target == e.kind }

func invalidf(format string, args ...any) error {
	return &kindError{ErrInvalid, fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &kindError{ErrNotFound, fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &kindError{ErrConflict, fmt.Sprintf(format, args...)}
}

// DomainTakenError says a domain belongs to another site. It matches
// ErrInvalid; callers decide whether the other site's name may be shown.
type DomainTakenError struct {
	Domain string
	Site   string
}

func (e *DomainTakenError) Error() string {
	return fmt.Sprintf("domain %q is already used by site %q", e.Domain, e.Site)
}
func (e *DomainTakenError) Is(target error) bool { return target == ErrInvalid }

// ValidateName checks a site name. Names end up as directory names, so the
// allowed alphabet is intentionally narrow.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return invalidf("invalid site name %q: use lowercase letters, digits and hyphens (max 63 characters)", name)
	}
	return nil
}

// NormalizeDomain lowercases a domain and checks it is a plain hostname.
func NormalizeDomain(d string) (string, error) {
	d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
	if d == "" || len(d) > 253 {
		return "", invalidf("invalid domain %q", d)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", invalidf("invalid domain %q: must contain at least one dot", d)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return "", invalidf("invalid domain %q", d)
		}
	}
	return d, nil
}

// NormalizeDomains normalizes, deduplicates and sorts a domain list.
func NormalizeDomains(domains []string) ([]string, error) {
	seen := map[string]bool{}
	clean := []string{}
	for _, d := range domains {
		n, err := NormalizeDomain(d)
		if err != nil {
			return nil, err
		}
		if !seen[n] {
			seen[n] = true
			clean = append(clean, n)
		}
	}
	slices.Sort(clean)
	return clean, nil
}
