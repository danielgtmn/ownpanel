package dav

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

func (h *Handler) report(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ == resNone || !res.exists {
		http.NotFound(w, r)
		return
	}
	root, ok := readXML(w, r)
	if !ok {
		return
	}
	if root == nil {
		http.Error(w, "A report needs a request body.", http.StatusBadRequest)
		return
	}
	pc := h.newPropCtx(rq)
	switch root.name {
	case cn("calendar-query"), crn("addressbook-query"):
		if (root.name == cn("calendar-query")) != (res.kind == davobj.Calendar) || res.typ != resCollection && res.typ != resObject {
			writeError(w, http.StatusForbidden, dn("supported-report"), "")
			return
		}
		h.query(w, r, pc, res, root)
	case cn("calendar-multiget"), crn("addressbook-multiget"):
		h.multiget(w, pc, res, root)
	case dn("sync-collection"):
		if res.typ != resCollection {
			writeError(w, http.StatusForbidden, dn("supported-report"), "")
			return
		}
		h.syncCollection(w, pc, res, root)
	case dn("expand-property"):
		// Answered like PROPFIND of the named properties, without
		// expanding them (enough for the programs that ask).
		pr := propRequest{}
		for _, p := range root.children(dn("property")) {
			if name, ok := p.attrValue("name"); ok {
				ns, _ := p.attrValue("namespace")
				if ns == "" {
					ns = nsDAV
				}
				pr.names = append(pr.names, xmlName(ns, name))
			}
		}
		ms := newMS(w)
		if err := h.propfindOne(w, ms, pc, res, pr); err != nil {
			h.fail(w, err)
			return
		}
		ms.end(w, "")
	case dn("principal-property-search"), dn("principal-search-property-set"):
		// Searching other people comes with scheduling (G5).
		if root.name == dn("principal-search-property-set") {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<d:principal-search-property-set xmlns:d="DAV:"/>` + "\n"))
			return
		}
		newMS(w).end(w, "")
	default:
		writeError(w, http.StatusForbidden, dn("supported-report"), "")
	}
}

// ---- calendar-query, addressbook-query ----

func (h *Handler) query(w http.ResponseWriter, r *http.Request, pc *propCtx, res *resource, root *node) {
	rq := pc.rq
	pr := parsePropRequest(root)
	filter := root.child(cn("filter"))
	if res.kind == davobj.AddressBook {
		filter = root.child(crn("filter"))
	}
	var calFilter *davobj.CompFilter
	var cardFilter davobj.CardFilter
	limit := 0
	if res.kind == davobj.Calendar {
		if filter == nil || len(filter.kids) != 1 {
			writeError(w, http.StatusForbidden, cn("valid-filter"), "")
			return
		}
		if countFilterElements(filter, 0) > davobj.MaxFilterElements*2 {
			writeError(w, http.StatusForbidden, cn("supported-filter"), "")
			return
		}
		f, err := parseCompFilter(filter.kids[0])
		if err != nil {
			writeError(w, http.StatusForbidden, cn("valid-filter"), "")
			return
		}
		if davobj.CheckFilter(f) != nil {
			// Too many filters, or nested too deeply.
			writeError(w, http.StatusForbidden, cn("supported-filter"), "")
			return
		}
		calFilter = &f
	} else {
		var err error
		cardFilter, err = parseCardFilter(filter)
		if err != nil {
			writeError(w, http.StatusForbidden, crn("supported-filter"), "")
			return
		}
		if l := root.child(crn("limit")); l != nil {
			if n, err := strconv.Atoi(strings.TrimSpace(l.child(crn("nresults")).textOrEmpty())); err == nil && n > 0 {
				limit = n
			}
		}
	}
	depth := r.Header.Get("Depth")
	var candidates []*resource
	collect := func(child *resource) error {
		candidates = append(candidates, child)
		return nil
	}
	switch {
	case res.typ == resObject:
		candidates = []*resource{res}
	case depth == "0":
	default:
		var objs []storage.DAVObject
		if res.dir == nil {
			// The index narrows a time range on events and tasks first.
			start, end := int64(0), int64(0)
			if tr := indexRange(calFilter); tr != nil {
				start, end = davobj.NoStart+1, davobj.NoEnd
				if !tr.Start.IsZero() {
					start = tr.Start.Unix()
				}
				if !tr.End.IsZero() {
					end = tr.End.Unix()
				}
			}
			var err error
			if objs, err = rq.st.DAVObjects(rq.ctx, *res.coll, start, end); err != nil {
				h.fail(w, err)
				return
			}
			if objs == nil {
				objs = []storage.DAVObject{}
			}
		}
		if err := h.eachObject(rq, res, objs, collect); err != nil {
			h.fail(w, err)
			return
		}
	}
	ms := newMS(w)
	matched := 0
	matcher := davobj.NewMatcher(rq.ctx, h.opts.QueryBudget)
	for _, c := range candidates {
		if rq.ctx.Err() != nil {
			return // the client is gone
		}
		data, err := pc.objectData(c)()
		if errors.Is(err, storage.ErrNoObject) {
			continue
		}
		if err != nil {
			h.logOrFail(w, ms, err)
			return
		}
		if calFilter != nil {
			cal, err := davobj.DecodeCalendar(data)
			if err != nil {
				continue
			}
			ok, err := matcher.Match(cal, *calFilter)
			matcher.Forget()
			switch {
			case errors.Is(err, davobj.ErrQueryBudget):
				// RFC 6578 section 3.6's way of saying "not all of it".
				if !ms.started {
					writeError(w, http.StatusInsufficientStorage, dn("number-of-matches-within-limits"), "")
					return
				}
				ms.status(w, res.path, http.StatusInsufficientStorage)
				ms.end(w, "")
				return
			case err != nil:
				return // the client is gone
			case !ok:
				continue
			}
		} else {
			card, err := davobj.DecodeCard(data)
			if err != nil || !davobj.MatchCard(card, cardFilter) {
				continue
			}
		}
		if limit > 0 && matched >= limit {
			ms.status(w, res.path, http.StatusInsufficientStorage)
			break
		}
		matched++
		props, err := pc.props(c, pr)
		if err != nil {
			h.logOrFail(w, ms, err)
			return
		}
		ms.response(w, c.path, props)
	}
	ms.end(w, "")
}

// countFilterElements counts the elements of a filter, bounded early.
func countFilterElements(n *node, acc int) int {
	acc++
	for _, k := range n.kids {
		if acc > 1000 {
			return acc
		}
		acc = countFilterElements(k, acc)
	}
	return acc
}

func (n *node) textOrEmpty() string {
	if n == nil {
		return ""
	}
	return n.text.String()
}

func (h *Handler) logOrFail(w http.ResponseWriter, ms *msWriter, err error) {
	if ms.started {
		h.log.Error("mail: calendar or contacts report failed", "err", err)
		ms.end(w, "")
		return
	}
	h.fail(w, err)
}

// indexRange is the time range of a query on events or tasks
// (VCALENDAR > VEVENT|VTODO with time-range), which the index can narrow.
func indexRange(f *davobj.CompFilter) *davobj.TimeRange {
	if f == nil || f.Name != "VCALENDAR" || len(f.Comps) != 1 {
		return nil
	}
	c := f.Comps[0]
	if c.IsNotDefined || c.TimeRange == nil || c.Name != davobj.CompEvent && c.Name != davobj.CompToDo {
		return nil
	}
	return c.TimeRange
}

const utcFormat = "20060102T150405Z"

func parseTimeRange(n *node) (*davobj.TimeRange, error) {
	tr := &davobj.TimeRange{}
	s, hasStart := n.attrValue("start")
	e, hasEnd := n.attrValue("end")
	if !hasStart && !hasEnd {
		return nil, errors.New("time-range without start and end")
	}
	var err error
	if hasStart {
		if tr.Start, err = time.Parse(utcFormat, s); err != nil {
			return nil, err
		}
	}
	if hasEnd {
		if tr.End, err = time.Parse(utcFormat, e); err != nil {
			return nil, err
		}
	}
	if hasStart && hasEnd && !tr.End.After(tr.Start) {
		return nil, errors.New("empty time range")
	}
	return tr, nil
}

func parseTextMatch(n *node, cardDefault bool) (*davobj.TextMatch, error) {
	tm := &davobj.TextMatch{Text: n.text.String()}
	tm.Collation, _ = n.attrValue("collation")
	if !davobj.KnownCollation(tm.Collation) {
		return nil, errors.New("unsupported collation")
	}
	if tm.Collation == "" && cardDefault {
		tm.Collation = davobj.CollationUnicode
	}
	if v, ok := n.attrValue("negate-condition"); ok && v == "yes" {
		tm.Negate = true
	}
	tm.MatchType, _ = n.attrValue("match-type")
	return tm, nil
}

func parseParamFilter(n *node, ns string) (davobj.ParamFilter, error) {
	name, _ := n.attrValue("name")
	pf := davobj.ParamFilter{Name: strings.ToUpper(name)}
	if n.child(xmlName(ns, "is-not-defined")) != nil {
		pf.IsNotDefined = true
	}
	if t := n.child(xmlName(ns, "text-match")); t != nil {
		tm, err := parseTextMatch(t, ns == nsCard)
		if err != nil {
			return pf, err
		}
		pf.TextMatch = tm
	}
	return pf, nil
}

func parseCompFilter(n *node) (davobj.CompFilter, error) {
	if n.name != cn("comp-filter") {
		return davobj.CompFilter{}, errors.New("expected comp-filter")
	}
	name, _ := n.attrValue("name")
	f := davobj.CompFilter{Name: strings.ToUpper(name)}
	for _, k := range n.kids {
		switch k.name {
		case cn("is-not-defined"):
			f.IsNotDefined = true
		case cn("time-range"):
			tr, err := parseTimeRange(k)
			if err != nil {
				return f, err
			}
			f.TimeRange = tr
		case cn("comp-filter"):
			c, err := parseCompFilter(k)
			if err != nil {
				return f, err
			}
			f.Comps = append(f.Comps, c)
		case cn("prop-filter"):
			pname, _ := k.attrValue("name")
			pf := davobj.PropFilter{Name: strings.ToUpper(pname)}
			for _, kk := range k.kids {
				switch kk.name {
				case cn("is-not-defined"):
					pf.IsNotDefined = true
				case cn("time-range"):
					tr, err := parseTimeRange(kk)
					if err != nil {
						return f, err
					}
					pf.TimeRange = tr
				case cn("text-match"):
					tm, err := parseTextMatch(kk, false)
					if err != nil {
						return f, err
					}
					pf.TextMatch = tm
				case cn("param-filter"):
					p, err := parseParamFilter(kk, nsCal)
					if err != nil {
						return f, err
					}
					pf.Params = append(pf.Params, p)
				}
			}
			f.Props = append(f.Props, pf)
		}
	}
	return f, nil
}

func parseCardFilter(n *node) (davobj.CardFilter, error) {
	f := davobj.CardFilter{}
	if n == nil {
		return f, nil
	}
	if v, _ := n.attrValue("test"); v == "allof" {
		f.AllOf = true
	}
	for _, k := range n.children(crn("prop-filter")) {
		name, _ := k.attrValue("name")
		pf := davobj.CardPropFilter{Name: name}
		if v, _ := k.attrValue("test"); v == "allof" {
			pf.AllOf = true
		}
		for _, kk := range k.kids {
			switch kk.name {
			case crn("is-not-defined"):
				pf.IsNotDefined = true
			case crn("text-match"):
				tm, err := parseTextMatch(kk, true)
				if err != nil {
					return f, err
				}
				pf.TextMatches = append(pf.TextMatches, *tm)
			case crn("param-filter"):
				p, err := parseParamFilter(kk, nsCard)
				if err != nil {
					return f, err
				}
				pf.Params = append(pf.Params, p)
			}
		}
		f.Props = append(f.Props, pf)
	}
	return f, nil
}

// ---- multiget ----

func (h *Handler) multiget(w http.ResponseWriter, pc *propCtx, res *resource, root *node) {
	pr := parsePropRequest(root)
	ms := newMS(w)
	for _, hn := range root.children(dn("href")) {
		if pc.rq.ctx.Err() != nil {
			return // the client is gone
		}
		target, raw, err := h.resolveHref(pc.rq, hn.text.String())
		if err != nil {
			h.logOrFail(w, ms, err)
			return
		}
		if target.typ != resObject || !target.exists || target.kind != res.kind {
			ms.status(w, raw, http.StatusNotFound)
			continue
		}
		props, err := pc.props(target, pr)
		if err != nil {
			h.logOrFail(w, ms, err)
			return
		}
		ms.response(w, target.path, props)
	}
	ms.end(w, "")
}

// ---- sync-collection ----

func (h *Handler) syncCollection(w http.ResponseWriter, pc *propCtx, res *resource, root *node) {
	rq := pc.rq
	pr := parsePropRequest(root)
	token := strings.TrimSpace(root.child(dn("sync-token")).textOrEmpty())
	if lvl := strings.TrimSpace(root.child(dn("sync-level")).textOrEmpty()); lvl != "" && lvl != "1" && lvl != "infinite" {
		http.Error(w, "The sync level must be 1 or infinite.", http.StatusBadRequest)
		return
	}
	if res.dir != nil {
		// The directory is generated: a token of its current state gives no
		// changes, any other one a full sync.
		switch token {
		case res.dir.token():
			newMS(w).end(w, res.dir.token())
			return
		case "":
		default:
			writeError(w, http.StatusForbidden, dn("valid-sync-token"), "")
			return
		}
		ms := newMS(w)
		err := h.eachObject(rq, res, nil, func(child *resource) error {
			props, err := pc.props(child, pr)
			if err == nil {
				ms.response(w, child.path, props)
			}
			return err
		})
		if err != nil {
			h.logOrFail(w, ms, err)
			return
		}
		ms.end(w, res.dir.token())
		return
	}
	changed, deleted, err := rq.st.DAVChanges(rq.ctx, *res.coll, token)
	if errors.Is(err, storage.ErrInvalidSyncToken) {
		writeError(w, http.StatusForbidden, dn("valid-sync-token"), "")
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	ms := newMS(w)
	if changed == nil {
		changed = []storage.DAVObject{}
	}
	err = h.eachObject(rq, res, changed, func(child *resource) error {
		props, err := pc.props(child, pr)
		if err == nil {
			ms.response(w, child.path, props)
		}
		return err
	})
	if err != nil {
		h.logOrFail(w, ms, err)
		return
	}
	for _, name := range deleted {
		ms.status(w, objectPath(res.owner, res.kind, res.coll.Name, name), http.StatusNotFound)
	}
	// The token is that of the newest change listed, read before the
	// changes: a change made meanwhile is listed again next time.
	ms.end(w, res.coll.SyncToken())
}
