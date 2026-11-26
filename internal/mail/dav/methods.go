package dav

import (
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// readObject reads the body of a PUT: at most davobj.MaxObjectBytes.
// tooLarge is set when it is larger; ok false when it could not be read
// (answered).
func readObject(w http.ResponseWriter, r *http.Request) (data []byte, tooLarge, ok bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, davobj.MaxObjectBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, true, true
		}
		http.Error(w, "The request body could not be read.", http.StatusBadRequest)
		return nil, false, false
	}
	if len(b) > davobj.MaxObjectBytes {
		return nil, true, true
	}
	return b, false, true
}

// etagList reads an If-Match or If-None-Match header: "*" or quoted
// ETags (weak ones compare like strong ones, as they are never weak
// here).
func etagList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "*" {
			out = append(out, "*")
			continue
		}
		part = strings.TrimPrefix(part, "W/")
		out = append(out, strings.Trim(part, `"`))
	}
	return out
}

func conditions(r *http.Request) storage.PutCondition {
	return storage.PutCondition{IfMatch: etagList(r.Header.Get("If-Match")), IfNoneMatch: etagList(r.Header.Get("If-None-Match"))}
}

// ---- GET, HEAD ----

func (h *Handler) get(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	switch {
	case res.typ == resNone || !res.exists:
		http.NotFound(w, r)
	case res.typ == resObject:
		etag, _, comp, mod := objectFacts(res)
		var data []byte
		if res.card != nil {
			data = res.card.data
		} else {
			var err error
			data, err = rq.st.ReadDAVObject(*res.obj)
			if errors.Is(err, storage.ErrNoObject) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				h.fail(w, err)
				return
			}
		}
		hd := w.Header()
		hd.Set("ETag", `"`+etag+`"`)
		hd.Set("Last-Modified", mod.UTC().Format(httpDate))
		hd.Set("Content-Type", contentType(res.kind, comp))
		for _, v := range etagList(r.Header.Get("If-None-Match")) {
			if v == "*" || v == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		hd.Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write(data)
		}
	case res.typ == resCollection:
		// The whole calendar or address book as one file.
		hd := w.Header()
		hd.Set("Content-Type", exportType(res.kind))
		hd.Set("Content-Disposition", `attachment; filename="`+ExportFileName(collectionTitle(res), res.kind)+`"`)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if res.dir != nil {
			for _, c := range res.dir.cards {
				if err := davobj.WriteCard(w, c.data); err != nil {
					return
				}
			}
			return
		}
		if err := Export(rq.ctx, rq.st, *res.coll, w); err != nil {
			h.log.Error("mail: export of a calendar or address book failed", "err", err)
		}
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "This address is for calendar and contacts programs.\n")
	}
}

func collectionTitle(res *resource) string {
	switch {
	case res.dir != nil:
		return directoryDisplayName
	case res.coll != nil && res.coll.DisplayName != "":
		return res.coll.DisplayName
	}
	return res.name
}

// ---- PUT ----

var (
	preValidCalData   = cn(davobj.ValidCalendarData)
	preValidAddrData  = crn(davobj.ValidAddressData)
	preQuotaExceeded  = dn("quota-not-exceeded")
	preNeedPrivileges = dn("need-privileges")
)

func preconditionName(kind, pre string) xml.Name {
	if kind == davobj.Calendar {
		return cn(pre)
	}
	return crn(pre)
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ != resObject {
		if res.typ == resNone {
			http.NotFound(w, r)
		} else {
			w.Header().Set("Allow", allowHeader)
			http.Error(w, "Only calendar objects and contacts can be stored here.", http.StatusMethodNotAllowed)
		}
		return
	}
	if res.dir != nil || !res.privs.Has(PrivWriteContent) || !res.exists && !res.privs.Has(PrivBind) {
		writeError(w, http.StatusForbidden, preNeedPrivileges, "")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		okType := err == nil && (res.kind == davobj.Calendar && mt == davobj.CalendarType ||
			res.kind == davobj.AddressBook && (mt == davobj.CardType || mt == "text/x-vcard"))
		if !okType {
			pre := davobj.SupportedCalendarData
			if res.kind == davobj.AddressBook {
				pre = davobj.SupportedAddressData
			}
			writeError(w, http.StatusUnsupportedMediaType, preconditionName(res.kind, pre), "")
			return
		}
	}
	data, tooLarge, ok := readObject(w, r)
	if !ok {
		return
	}
	if tooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, preconditionName(res.kind, davobj.MaxResourceSize), "")
		return
	}
	var info davobj.Info
	var err error
	if res.kind == davobj.Calendar {
		info, _, err = davobj.ParseCalendar(data, res.coll.Components)
	} else {
		info, _, err = davobj.ParseCard(data)
	}
	var inv *davobj.Error
	if errors.As(err, &inv) {
		writeError(w, http.StatusForbidden, preconditionName(res.kind, inv.Precondition), "")
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	obj, created, err := rq.st.PutDAVObject(rq.ctx, *res.coll, res.name, data, info, conditions(r))
	if !h.storeError(w, res, err) {
		return
	}
	w.Header().Set("ETag", `"`+obj.ETag()+`"`)
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// storeError answers for an error of a change; true when there was none.
func (h *Handler) storeError(w http.ResponseWriter, res *resource, err error) bool {
	var uc *storage.UIDConflictError
	switch {
	case err == nil:
		return true
	case errors.Is(err, storage.ErrPrecondition):
		http.Error(w, "The object was changed or created meanwhile.", http.StatusPreconditionFailed)
	case errors.As(err, &uc):
		writeError(w, http.StatusForbidden, preconditionName(res.kind, "no-uid-conflict"), href(objectPath(res.owner, res.kind, res.coll.Name, uc.Name)))
	case errors.Is(err, storage.ErrOverQuota):
		writeError(w, http.StatusInsufficientStorage, preQuotaExceeded, "")
	case errors.Is(err, storage.ErrTooManyObjects), errors.Is(err, storage.ErrTooManyCollections):
		http.Error(w, "There are too many objects here.", http.StatusInsufficientStorage)
	case errors.Is(err, storage.ErrNoObject), errors.Is(err, storage.ErrNoCollection):
		http.Error(w, "Not found.", http.StatusNotFound)
	case errors.Is(err, storage.ErrInvalidDAVName):
		http.Error(w, "This name cannot be used.", http.StatusForbidden)
	default:
		h.fail(w, err)
	}
	return false
}

// ---- DELETE ----

func (h *Handler) delete(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ == resNone || !res.exists {
		http.NotFound(w, r)
		return
	}
	switch {
	case res.typ == resObject && res.dir == nil && res.privs.Has(PrivUnbind|PrivWriteContent):
		if !h.storeError(w, res, rq.st.DeleteDAVObject(rq.ctx, *res.coll, res.name, conditions(r))) {
			return
		}
	case res.typ == resCollection && res.dir == nil && res.privs.Has(PrivAll):
		if !h.storeError(w, res, rq.st.DeleteDAVCollection(rq.ctx, res.owner.StorageID, res.coll.ID)) {
			return
		}
	default:
		writeError(w, http.StatusForbidden, preNeedPrivileges, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- MKCALENDAR, MKCOL ----

// collNameRe are the names a new collection may have.
var collNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+~-]{0,127}$`)

func (h *Handler) mkcol(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ == resNone {
		http.NotFound(w, r)
		return
	}
	if res.typ != resCollection || res.exists {
		w.Header().Set("Allow", allowHeader)
		http.Error(w, "A collection cannot be created here.", http.StatusMethodNotAllowed)
		return
	}
	if !res.privs.Has(PrivBind) {
		writeError(w, http.StatusForbidden, preNeedPrivileges, "")
		return
	}
	if !collNameRe.MatchString(res.name) || res.name == directoryName {
		http.Error(w, "This name cannot be used for a collection.", http.StatusForbidden)
		return
	}
	root, ok := readXML(w, r)
	if !ok {
		return
	}
	c := storage.DAVCollection{Kind: res.kind, Name: res.name}
	var set *node
	if r.Method == "MKCALENDAR" {
		if res.kind != davobj.Calendar {
			http.Error(w, "Calendars are created in the calendar home.", http.StatusForbidden)
			return
		}
		if root != nil {
			if root.name != cn("mkcalendar") {
				http.Error(w, "Expected a mkcalendar element.", http.StatusBadRequest)
				return
			}
			set = root.child(dn("set")).child(dn("prop"))
		}
	} else {
		// Extended MKCOL (RFC 5689) with the resource type of the home.
		if root == nil || root.name != dn("mkcol") {
			http.Error(w, "Only calendars and address books can be created here.", http.StatusForbidden)
			return
		}
		set = root.child(dn("set")).child(dn("prop"))
		rt := set.child(pResourceType)
		want := cn("calendar")
		if res.kind == davobj.AddressBook {
			want = crn("addressbook")
		}
		if rt == nil || rt.child(dn("collection")) == nil || rt.child(want) == nil {
			writeError(w, http.StatusForbidden, dn("valid-resourcetype"), "")
			return
		}
	}
	for _, p := range set.kidsOrNil() {
		v := strings.TrimSpace(p.text.String())
		switch {
		case p.name == pDisplayName && oneLine(v, maxDisplayName):
			c.DisplayName = v
		case (p.name == pCalDescription || p.name == pCardDescription) && len(v) <= maxDescription:
			c.Description = v
		case p.name == pCalColor && colorRe.MatchString(v):
			c.Color = v
		case p.name == pSupportedCalComps && res.kind == davobj.Calendar:
			for _, comp := range p.children(cn("comp")) {
				name, _ := comp.attrValue("name")
				name = strings.ToUpper(name)
				if (name == davobj.CompEvent || name == davobj.CompToDo) && !containsString(c.Components, name) {
					c.Components = append(c.Components, name)
				}
			}
			if len(c.Components) == 0 {
				writeError(w, http.StatusForbidden, cn(davobj.SupportedComponent), "")
				return
			}
		}
	}
	if len(c.Components) == 2 {
		c.Components = nil
	}
	_, err := rq.st.CreateDAVCollection(rq.ctx, res.owner.StorageID, c)
	switch {
	case errors.Is(err, storage.ErrCollectionExists):
		http.Error(w, "The collection exists already.", http.StatusMethodNotAllowed)
		return
	case !h.storeError(w, res, err):
		return
	}
	w.Header().Set("Location", res.path)
	w.WriteHeader(http.StatusCreated)
}

func (n *node) kidsOrNil() []*node {
	if n == nil {
		return nil
	}
	return n.kids
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
