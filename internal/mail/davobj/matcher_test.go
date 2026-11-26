package davobj

import (
	"context"
	"errors"
	"testing"
	"time"
)

func manyRanges(n int) CompFilter {
	f := CompFilter{Name: "VCALENDAR"}
	for i := range n {
		f.Comps = append(f.Comps, CompFilter{Name: CompEvent, TimeRange: &TimeRange{
			Start: time.Date(2027, 1, 1+i%28, 0, 0, 0, 0, time.UTC), End: time.Date(2027, 1, 2+i%28, 0, 0, 0, 0, time.UTC)}})
	}
	return f
}

// TestMatcherExpandsOnce: however many time ranges ask, a component's
// recurrence is expanded once per request.
func TestMatcherExpandsOnce(t *testing.T) {
	_, c, err := ParseCalendar(cal(ev("DTSTART:20260101T000000Z\r\nDTEND:20260101T003000Z\r\nRRULE:FREQ=HOURLY\r\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(context.Background(), 0)
	ok, err := m.Match(c, manyRanges(MaxFilterElements-1))
	if err != nil || !ok || m.Expansions() != 1 {
		t.Fatalf("%v %v, %d expansions", ok, err, m.Expansions())
	}
}

func TestMatcherBudgetAndContext(t *testing.T) {
	_, c, err := ParseCalendar(cal(ev("DTSTART:20260101T000000Z\r\nRRULE:FREQ=DAILY\r\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := manyRanges(1)
	m := NewMatcher(context.Background(), time.Nanosecond)
	if _, err := m.Match(c, f); err != nil {
		t.Fatal(err) // the first expansion always runs
	}
	m.Forget()
	if _, err := m.Match(c, f); !errors.Is(err, ErrQueryBudget) {
		t.Fatalf("over budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewMatcher(ctx, 0).Match(c, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestCheckFilter(t *testing.T) {
	if err := CheckFilter(manyRanges(MaxFilterElements - 1)); err != nil {
		t.Fatal(err)
	}
	if err := CheckFilter(manyRanges(MaxFilterElements)); !errors.Is(err, ErrFilterTooComplex) {
		t.Fatal(err)
	}
	deep := CompFilter{Name: "X"}
	for range MaxFilterDepth {
		deep = CompFilter{Name: "X", Comps: []CompFilter{deep}}
	}
	if err := CheckFilter(deep); !errors.Is(err, ErrFilterTooComplex) {
		t.Fatal(err)
	}
}
