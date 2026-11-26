package davobj

import (
	"strings"
	"testing"
	"time"
)

func cal(body string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n" + body + "END:VCALENDAR\r\n")
}

func ev(props string) string {
	return "BEGIN:VEVENT\r\nUID:u\r\nDTSTAMP:20240101T000000Z\r\n" + props + "END:VEVENT\r\n"
}

func TestSpans(t *testing.T) {
	utc := func(s string) int64 {
		v, err := time.Parse("20060102T150405Z", s)
		if err != nil {
			t.Fatal(err)
		}
		return v.Unix()
	}
	widen := int64(floatWiden / time.Second)
	cases := []struct {
		name       string
		body       string
		start, end int64
	}{
		{"utc", ev("DTSTART:20240101T090000Z\r\nDTEND:20240101T100000Z\r\n"), utc("20240101T090000Z"), utc("20240101T100000Z")},
		{"tzid", ev("DTSTART;TZID=Europe/Berlin:20240701T090000\r\nDURATION:PT2H\r\n"), utc("20240701T070000Z"), utc("20240701T090000Z")},
		{"mozilla tzid", ev("DTSTART;TZID=/mozilla.org/20050126_1/America/New_York:20240115T090000\r\n"), utc("20240115T140000Z"), utc("20240115T140000Z")},
		{"count", ev("DTSTART:20240101T090000Z\r\nDTEND:20240101T100000Z\r\nRRULE:FREQ=DAILY;COUNT=3\r\n"), utc("20240101T090000Z"), utc("20240103T100000Z")},
		{"until", ev("DTSTART:20240101T090000Z\r\nRRULE:FREQ=WEEKLY;UNTIL=20240115T090000Z\r\n"), utc("20240101T090000Z"), utc("20240115T090000Z")},
		{"forever", ev("DTSTART:20240101T090000Z\r\nRRULE:FREQ=WEEKLY\r\n"), utc("20240101T090000Z"), NoEnd},
		{"count with exdate", ev("DTSTART:20240101T090000Z\r\nRRULE:FREQ=YEARLY;COUNT=3\r\n"), utc("20240101T090000Z"), utc("20260101T090000Z")},
		{"floating", ev("DTSTART:20240101T090000\r\n"), utc("20240101T090000Z") - widen, utc("20240101T090000Z") + widen},
		{"all day", ev("DTSTART;VALUE=DATE:20240101\r\n"), utc("20240101T000000Z") - widen, utc("20240102T000000Z") + widen},
		{"task without dates", "BEGIN:VTODO\r\nUID:u\r\nDTSTAMP:20240101T000000Z\r\nEND:VTODO\r\n", NoStart, NoEnd},
		{"task due", "BEGIN:VTODO\r\nUID:u\r\nDTSTAMP:20240101T000000Z\r\nDUE:20240301T000000Z\r\nEND:VTODO\r\n", utc("20240301T000000Z"), utc("20240301T000000Z")},
	}
	for _, c := range cases {
		info, _, err := ParseCalendar(cal(c.body), nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if info.Start != c.start || info.End != c.end || info.UID != "u" {
			t.Errorf("%s: %d..%d, want %d..%d", c.name, info.Start, info.End, c.start, c.end)
		}
	}
}

func TestTimezoneFallbacks(t *testing.T) {
	custom := "BEGIN:VTIMEZONE\r\nTZID:My Zone\r\nX-LIC-LOCATION:Asia/Tokyo\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0900\r\nTZOFFSETTO:+0900\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	offsetOnly := "BEGIN:VTIMEZONE\r\nTZID:Somewhere\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0530\r\nTZOFFSETTO:+0530\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	info, _, err := ParseCalendar(cal(custom+ev("DTSTART;TZID=My Zone:20240101T090000\r\n")), nil)
	if err != nil || info.Start != time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("X-LIC-LOCATION: %+v %v", info, err)
	}
	info, _, err = ParseCalendar(cal(offsetOnly+ev("DTSTART;TZID=Somewhere:20240101T090000\r\n")), nil)
	want := time.Date(2024, 1, 1, 3, 30, 0, 0, time.UTC).Unix() - int64(floatWiden/time.Second)
	if err != nil || info.Start != want {
		t.Fatalf("offset only: %+v %v", info, err)
	}
	// An unknown zone is read as UTC, widened like a floating time.
	if _, _, err := ParseCalendar(cal(ev("DTSTART;TZID=Nowhere/Land:20240101T090000\r\n")), nil); err != nil {
		t.Fatal(err)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string][]byte{
		"empty":         []byte(""),
		"not text":      []byte("BEGIN:VCALENDAR\r\n\x00\r\nEND:VCALENDAR\r\n"),
		"no event":      cal(""),
		"no uid":        cal("BEGIN:VEVENT\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240101T000000Z\r\nEND:VEVENT\r\n"),
		"no start":      cal("BEGIN:VEVENT\r\nUID:u\r\nDTSTAMP:20240101T000000Z\r\nEND:VEVENT\r\n"),
		"two masters":   cal(ev("DTSTART:20240101T000000Z\r\n") + ev("DTSTART:20240102T000000Z\r\n")),
		"mixed":         cal(ev("DTSTART:20240101T000000Z\r\n") + "BEGIN:VTODO\r\nUID:u\r\nDTSTAMP:20240101T000000Z\r\nEND:VTODO\r\n"),
		"two calendars": append(cal(ev("DTSTART:20240101T000000Z\r\n")), cal(ev("DTSTART:20240101T000000Z\r\n"))...),
		"deep":          cal(strings.Repeat("BEGIN:X\r\n", 20) + strings.Repeat("END:X\r\n", 20)),
		"bad rule":      cal(ev("DTSTART:20240101T000000Z\r\nRRULE:FREQ=SOMETIMES\r\n")),
		"bad date":      cal(ev("DTSTART:2024-01-01\r\n")),
		"huge params":   cal(ev("DTSTART;X=" + strings.Repeat("a", 10000) + ":20240101T000000Z\r\n")),
	}
	for name, data := range cases {
		if _, _, err := ParseCalendar(data, nil); err == nil || !IsInvalid(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := ParseCalendar(cal(ev("DTSTART:20240101T000000Z\r\n")), []string{CompToDo}); err == nil {
		t.Error("event in a task list")
	}
	if _, _, err := ParseCard([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:x\r\nEND:VCARD\r\n")); err == nil {
		t.Error("card without UID")
	}
}

// TestBoundedExpansion: rules that never produce an occurrence, or produce
// very many, finish quickly.
func TestBoundedExpansion(t *testing.T) {
	start := time.Now()
	for _, rule := range []string{
		"FREQ=SECONDLY",
		"FREQ=MINUTELY;BYMONTH=2;BYMONTHDAY=30;UNTIL=20300101T000000Z",
		"FREQ=SECONDLY;COUNT=1000000",
		"FREQ=MINUTELY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=SECONDLY;BYMONTH=2;BYMONTHDAY=31;COUNT=3",
	} {
		data := cal(ev("DTSTART:20240101T000000Z\r\nRRULE:" + rule + "\r\n"))
		info, c, err := ParseCalendar(data, nil)
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		f := CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{Start: time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2029, 1, 2, 0, 0, 0, 0, time.UTC)}}}}
		MatchCalendar(c, f)
		_ = info
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestOverlaps(t *testing.T) {
	data := cal(ev("DTSTART;TZID=Europe/Berlin:20240101T090000\r\nDTEND;TZID=Europe/Berlin:20240101T100000\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE;TZID=Europe/Berlin:20240108T090000\r\n"))
	_, c, err := ParseCalendar(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := func(s, e string) bool {
		a, _ := time.Parse("20060102T150405Z", s)
		b, _ := time.Parse("20060102T150405Z", e)
		return MatchCalendar(c, CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: CompEvent, TimeRange: &TimeRange{Start: a, End: b}}}})
	}
	if !q("20240101T080000Z", "20240101T081000Z") {
		t.Error("first occurrence (09:00 Berlin is 08:00 UTC in winter)")
	}
	if q("20240108T000000Z", "20240109T000000Z") {
		t.Error("excluded occurrence")
	}
	if !q("20240715T070000Z", "20240715T071000Z") {
		t.Error("summer occurrence (09:00 Berlin is 07:00 UTC)")
	}
	if q("20240715T080000Z", "20240715T090000Z") {
		t.Error("an hour after the summer occurrence")
	}
}

func TestSplitAndJoin(t *testing.T) {
	ics := cal("BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		ev("DTSTART;TZID=Europe/Berlin:20240101T090000\r\nSUMMARY:A long summary that is certainly longer than seventy-five octets so it gets folded äöü\r\n") +
		"BEGIN:VEVENT\r\nUID:v\r\nDTSTAMP:20240101T000000Z\r\nDTSTART:20240102T000000Z\r\nEND:VEVENT\r\n")
	objs, skipped, err := SplitCalendars(ics)
	if err != nil || len(objs) != 2 || skipped != 0 {
		t.Fatalf("%d %d %v", len(objs), skipped, err)
	}
	if !strings.Contains(string(objs[0]), "BEGIN:VTIMEZONE") || strings.Contains(string(objs[1]), "BEGIN:VTIMEZONE") {
		t.Fatalf("time zones: %s / %s", objs[0], objs[1])
	}
	for _, line := range strings.Split(string(objs[0]), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line not folded: %q", line)
		}
	}
	info, c, err := ParseCalendar(objs[0], nil)
	if err != nil || info.UID != "u" {
		t.Fatal(err)
	}
	if s, _ := c.Children[1].Props.Text("SUMMARY"); !strings.HasSuffix(s, "äöü") {
		t.Fatalf("summary %q", s)
	}
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nUID:b\r\nFN:B\r\nPHOTO;ENCODING=b:\r\n QUJD\r\nEND:VCARD\r\n"
	cards, skipped, err := SplitCards([]byte(vcf))
	if err != nil || len(cards) != 2 || skipped != 0 {
		t.Fatalf("%d %d %v", len(cards), skipped, err)
	}
	if !strings.Contains(string(cards[0]), "UID:") || !strings.Contains(string(cards[1]), " QUJD") {
		t.Fatalf("%q", cards)
	}
}
