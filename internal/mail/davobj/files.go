package davobj

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

// Import and export of whole calendars (.ics) and address books (.vcf).

// ProdID is the PRODID of calendars written here.
const ProdID = "-//ownpanel//calendar//EN"

// newUID returns a random UID for objects that came without one.
func newUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b) + "@ownpanel"
}

// SplitCalendars reads an .ics file (one or more VCALENDAR objects) and
// returns one calendar object resource per UID, with the time zones it
// uses. Events and tasks without a UID get one. Components that cannot be
// stored (other kinds, objects too large or not valid) are counted in
// skipped.
func SplitCalendars(data []byte) (objects [][]byte, skipped int, err error) {
	if len(data) > MaxFileBytes {
		return nil, 0, invalid(MaxResourceSize, "the file is larger than %d MB", MaxFileBytes>>20)
	}
	if err := checkText(data, ValidCalendarData); err != nil {
		return nil, 0, err
	}
	dec := ical.NewDecoder(bytes.NewReader(data))
	type group struct {
		comps []*ical.Component
	}
	var order []string
	groups := map[string]*group{}
	timezones := map[string]*ical.Component{}
	calendars := 0
	for {
		cal, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, invalid(ValidCalendarData, "the file cannot be read as a calendar: %v", shortErr(err))
		}
		calendars++
		for _, c := range cal.Children {
			switch c.Name {
			case ical.CompTimezone:
				if p := c.Props.Get(ical.PropTimezoneID); p != nil {
					if _, ok := timezones[p.Value]; !ok {
						timezones[p.Value] = c
					}
				}
				continue
			case CompEvent, CompToDo:
			default:
				skipped++
				continue
			}
			uid := ""
			if p := c.Props.Get(ical.PropUID); p != nil {
				uid = p.Value
			}
			if !ValidUID(uid) {
				uid = newUID()
				c.Props.SetText(ical.PropUID, uid)
			}
			key := c.Name + "\x00" + uid
			g := groups[key]
			if g == nil {
				g = &group{}
				groups[key] = g
				order = append(order, key)
			}
			g.comps = append(g.comps, c)
		}
	}
	if calendars == 0 {
		return nil, 0, invalid(ValidCalendarData, "the file holds no calendar")
	}
	for _, key := range order {
		g := groups[key]
		var buf bytes.Buffer
		w := bufio.NewWriter(&buf)
		writeLine(w, "BEGIN", nil, "VCALENDAR")
		writeLine(w, "VERSION", nil, "2.0")
		writeLine(w, "PRODID", nil, ProdID)
		for _, tzid := range tzidsOf(g.comps) {
			if tz, ok := timezones[tzid]; ok {
				writeComponent(w, tz)
			}
		}
		for _, c := range g.comps {
			writeComponent(w, c)
		}
		writeLine(w, "END", nil, "VCALENDAR")
		w.Flush()
		if _, _, err := ParseCalendar(buf.Bytes(), nil); err != nil {
			skipped++
			continue
		}
		objects = append(objects, buf.Bytes())
	}
	return objects, skipped, nil
}

// tzidsOf lists the TZIDs the components (and their children) refer to,
// sorted.
func tzidsOf(comps []*ical.Component) []string {
	seen := map[string]bool{}
	var walk func(c *ical.Component)
	walk = func(c *ical.Component) {
		for _, props := range c.Props {
			for _, p := range props {
				if id := p.Params.Get(ical.ParamTimezoneID); id != "" {
					seen[id] = true
				}
			}
		}
		for _, ch := range c.Children {
			walk(ch)
		}
	}
	for _, c := range comps {
		walk(c)
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// SplitCards reads a .vcf file (one or more vCards) and returns one vCard
// per contact, as written in the file; contacts without a UID get one.
// Contacts that cannot be stored are counted in skipped.
func SplitCards(data []byte) (cards [][]byte, skipped int, err error) {
	if len(data) > MaxFileBytes {
		return nil, 0, invalid(MaxResourceSize, "the file is larger than %d MB", MaxFileBytes>>20)
	}
	if err := checkText(data, ValidAddressData); err != nil {
		return nil, 0, err
	}
	var cur *bytes.Buffer
	found := 0
	rest := data
	for len(rest) > 0 {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		folded := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		switch {
		case !folded && strings.EqualFold(string(bytes.TrimSpace(trimmed)), "BEGIN:VCARD"):
			cur = &bytes.Buffer{}
			cur.Write(trimmed)
			cur.WriteString("\r\n")
		case cur == nil:
		case !folded && strings.EqualFold(string(bytes.TrimSpace(trimmed)), "END:VCARD"):
			found++
			card := cur.Bytes()
			if dc, err := DecodeCard(append(append([]byte{}, card...), "END:VCARD\r\n"...)); err == nil && len(dc["UID"]) == 0 {
				card = append(card, "UID:"+newUID()+"\r\n"...)
			}
			card = append(card, "END:VCARD\r\n"...)
			cur = nil
			if _, _, err := ParseCard(card); err != nil {
				skipped++
				continue
			}
			cards = append(cards, card)
		default:
			cur.Write(trimmed)
			cur.WriteString("\r\n")
		}
	}
	if found == 0 {
		return nil, 0, invalid(ValidAddressData, "the file holds no contacts")
	}
	return cards, skipped, nil
}

// CalendarWriter joins calendar objects into one .ics file, each time zone
// once.
type CalendarWriter struct {
	w       *bufio.Writer
	tz      map[string]bool
	started bool
	name    string
}

// NewCalendarWriter writes a calendar called name to w.
func NewCalendarWriter(w io.Writer, name string) *CalendarWriter {
	return &CalendarWriter{w: bufio.NewWriterSize(w, 64<<10), tz: map[string]bool{}, name: name}
}

func (cw *CalendarWriter) start() {
	if cw.started {
		return
	}
	cw.started = true
	writeLine(cw.w, "BEGIN", nil, "VCALENDAR")
	writeLine(cw.w, "VERSION", nil, "2.0")
	writeLine(cw.w, "PRODID", nil, ProdID)
	if cw.name != "" {
		writeLine(cw.w, "X-WR-CALNAME", nil, escapeText(cw.name))
	}
}

// Add adds a stored calendar object; one that cannot be read is left out
// (false).
func (cw *CalendarWriter) Add(data []byte) (bool, error) {
	cal, err := DecodeCalendar(data)
	if err != nil {
		return false, nil
	}
	cw.start()
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			id := ""
			if p := c.Props.Get(ical.PropTimezoneID); p != nil {
				id = p.Value
			}
			if cw.tz[id] {
				continue
			}
			cw.tz[id] = true
		}
		writeComponent(cw.w, c)
	}
	return true, cw.w.Flush()
}

// Close ends the calendar.
func (cw *CalendarWriter) Close() error {
	cw.start()
	writeLine(cw.w, "END", nil, "VCALENDAR")
	return cw.w.Flush()
}

// WriteCard writes a stored vCard to an address book file, ending it with
// a line break.
func WriteCard(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil {
		return err
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		_, err := io.WriteString(w, "\r\n")
		return err
	}
	return nil
}

func escapeText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)
	return r.Replace(s)
}

// writeComponent writes a component as it was read: properties in name
// order, values unchanged, lines folded at 75 octets.
func writeComponent(w *bufio.Writer, c *ical.Component) {
	writeLine(w, "BEGIN", nil, c.Name)
	names := make([]string, 0, len(c.Props))
	for n := range c.Props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, p := range c.Props[n] {
			writeLine(w, p.Name, p.Params, p.Value)
		}
	}
	for _, ch := range c.Children {
		writeComponent(w, ch)
	}
	writeLine(w, "END", nil, c.Name)
}

func writeLine(w *bufio.Writer, name string, params ical.Params, value string) {
	var b strings.Builder
	b.WriteString(name)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(";" + k + "=")
		for i, v := range params[k] {
			if i > 0 {
				b.WriteByte(',')
			}
			v = strings.Map(func(r rune) rune {
				if r == '"' || r == '\r' || r == '\n' {
					return -1
				}
				return r
			}, v)
			if strings.ContainsAny(v, ";:,") {
				b.WriteString(`"` + v + `"`)
			} else {
				b.WriteString(v)
			}
		}
	}
	b.WriteByte(':')
	b.WriteString(value)
	fold(w, b.String())
}

// fold writes a content line in lines of at most 75 octets, never inside
// a UTF-8 sequence.
func fold(w *bufio.Writer, line string) {
	first := true
	for len(line) > 0 {
		limit := 75
		if !first {
			limit = 74
			w.WriteByte(' ')
		}
		if len(line) <= limit {
			w.WriteString(line)
			break
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		w.WriteString(line[:cut])
		w.WriteString("\r\n")
		line = line[cut:]
		first = false
	}
	w.WriteString("\r\n")
}
