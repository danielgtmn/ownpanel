package davobj

import (
	"context"
	"errors"
	"time"

	"github.com/emersion/go-ical"
)

// Limits of one calendar query.
const (
	// MaxFilterElements bounds comp-filter, prop-filter and param-filter
	// elements of one query, all levels together.
	MaxFilterElements = 32
	// MaxFilterDepth bounds the nesting of comp-filters.
	MaxFilterDepth = 8
	// DefaultQueryBudget is the time one query may spend expanding
	// recurrences, over all objects it looks at.
	DefaultQueryBudget = 10 * time.Second
)

// ErrQueryBudget: the query needed more work than its budget allows.
var ErrQueryBudget = errors.New("the query needs too much work; ask for a shorter time range or fewer filters")

// ErrFilterTooComplex: more filter elements or deeper nesting than allowed.
var ErrFilterTooComplex = errors.New("the filter is too complex")

// CheckFilter refuses filters with more than MaxFilterElements elements or
// nested deeper than MaxFilterDepth.
func CheckFilter(f CompFilter) error {
	n := 0
	var walk func(f CompFilter, depth int) bool
	walk = func(f CompFilter, depth int) bool {
		n++
		if depth > MaxFilterDepth {
			return false
		}
		for _, p := range f.Props {
			n += 1 + len(p.Params)
		}
		for _, c := range f.Comps {
			if !walk(c, depth+1) {
				return false
			}
		}
		return n <= MaxFilterElements
	}
	if !walk(f, 1) || n > MaxFilterElements {
		return ErrFilterTooComplex
	}
	return nil
}

// Matcher evaluates calendar-query filters for one request: it reads each
// component's dates and expands its recurrence once, however many
// time-range filters ask, checks the request's context, and stops when the
// time spent expanding passes its budget.
type Matcher struct {
	ctx    context.Context
	budget time.Duration
	spent  time.Duration
	cache  map[*ical.Component]*expansion
	// expansions counts recurrence expansions (tests).
	expansions int
}

// expansion is what is known of one component's occurrences.
type expansion struct {
	t   timing
	err error
	// occ are the starts of the occurrences, in order, at most
	// MaxInstances+1; complete is set when the rule ended before that.
	occ      []time.Time
	expanded bool
	complete bool
}

// NewMatcher returns a matcher for one request; budget 0 means
// DefaultQueryBudget.
func NewMatcher(ctx context.Context, budget time.Duration) *Matcher {
	if budget <= 0 {
		budget = DefaultQueryBudget
	}
	return &Matcher{ctx: ctx, budget: budget, cache: map[*ical.Component]*expansion{}}
}

// Expansions is how many recurrence sets were expanded.
func (m *Matcher) Expansions() int { return m.expansions }

// Match tells whether the calendar object cal matches the filter (whose top
// comp-filter names VCALENDAR). It fails with the context's error or
// ErrQueryBudget. Components are cached by identity: decode each object
// once per request.
func (m *Matcher) Match(cal *ical.Calendar, f CompFilter) (bool, error) {
	if err := m.ctx.Err(); err != nil {
		return false, err
	}
	if cal.Name != f.Name {
		return f.IsNotDefined, nil
	}
	if f.IsNotDefined {
		return false, nil
	}
	return m.matchComp(cal.Component, f, newTZ(cal))
}

// Forget drops what is cached of an object's components (call it when
// done with an object, so memory stays bounded).
func (m *Matcher) Forget() { clear(m.cache) }

// MatchCalendar is Match with a background context and the default budget;
// a query over its budget counts as a match (clients filter again).
func MatchCalendar(cal *ical.Calendar, f CompFilter) bool {
	ok, err := NewMatcher(context.Background(), 0).Match(cal, f)
	return ok || err != nil
}

func (m *Matcher) matchComp(c *ical.Component, f CompFilter, tz *tzSet) (bool, error) {
	if err := m.ctx.Err(); err != nil {
		return false, err
	}
	if f.TimeRange != nil && (c.Name == CompEvent || c.Name == CompToDo) {
		ok, err := m.overlaps(c, tz, f.TimeRange.Start, f.TimeRange.End)
		if err != nil || !ok {
			return false, err
		}
	}
	for _, cf := range f.Comps {
		found := false
		for _, child := range c.Children {
			if child.Name != cf.Name {
				continue
			}
			if cf.IsNotDefined {
				found = true
				break
			}
			ok, err := m.matchComp(child, cf, tz)
			if err != nil {
				return false, err
			}
			if ok {
				found = true
				break
			}
		}
		if found == cf.IsNotDefined {
			return false, nil
		}
	}
	for _, pf := range f.Props {
		props := c.Props.Values(pf.Name)
		if pf.IsNotDefined {
			if len(props) > 0 {
				return false, nil
			}
			continue
		}
		found := false
		for i := range props {
			if matchProp(&props[i], pf) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// expansionOf reads a component's dates and expands its recurrence, once.
func (m *Matcher) expansionOf(c *ical.Component, tz *tzSet) (*expansion, error) {
	if e := m.cache[c]; e != nil {
		return e, nil
	}
	e := &expansion{}
	e.t, e.err = timingOf(c, tz)
	if e.err == nil && e.t.recur != nil {
		if m.spent >= m.budget {
			return nil, ErrQueryBudget
		}
		began := time.Now()
		m.expansions++
		next := e.t.recur.Iterator()
		for len(e.occ) <= MaxInstances {
			v, ok := next()
			if !ok {
				e.complete = true
				break
			}
			e.occ = append(e.occ, v)
			if len(e.occ)%1024 == 0 {
				if err := m.ctx.Err(); err != nil {
					return nil, err
				}
			}
		}
		e.expanded = true
		m.spent += time.Since(began)
	}
	m.cache[c] = e
	return e, nil
}

// overlaps tells whether a component has an occurrence in [qs, qe) (RFC
// 4791 section 9.9; a zero qs or qe is open). Occurrences beyond what was
// expanded count as overlapping, so nothing is ever missed.
func (m *Matcher) overlaps(c *ical.Component, tz *tzSet, qs, qe time.Time) (bool, error) {
	e, err := m.expansionOf(c, tz)
	if err != nil {
		return false, err
	}
	t := e.t
	if e.err != nil {
		return false, nil
	}
	if t.open {
		return true, nil
	}
	if t.widen {
		if !qs.IsZero() {
			qs = qs.Add(-floatWiden)
		}
		if !qe.IsZero() {
			qe = qe.Add(floatWiden)
		}
	}
	hit := func(s, e time.Time) bool {
		if e.Equal(s) {
			return (qs.IsZero() || !s.Before(qs)) && (qe.IsZero() || s.Before(qe))
		}
		return (qe.IsZero() || s.Before(qe)) && (qs.IsZero() || e.After(qs))
	}
	if t.unexpanded {
		// Any time from the start (to the last possible start).
		if !qe.IsZero() && !t.start.Before(qe) {
			return false, nil
		}
		return t.lastStart.IsZero() || qs.IsZero() || t.lastStart.Add(t.duration()).After(qs) || !t.lastStart.Before(qs), nil
	}
	if !e.expanded {
		return hit(t.start, t.end), nil
	}
	d := t.duration()
	for _, v := range e.occ {
		if !qe.IsZero() && !v.Before(qe) {
			return false, nil
		}
		if hit(v, v.Add(d)) {
			return true, nil
		}
	}
	if !e.complete {
		// More than MaxInstances occurrences: the rest is not known.
		return true, nil
	}
	// Occurrences beyond the horizon are not known: a range that reaches
	// past it may hold some.
	return !t.ended(len(e.occ)) && (qe.IsZero() || qe.After(t.capAt)), nil
}
