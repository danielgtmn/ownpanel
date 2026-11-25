package dav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/davobj"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// propRequest is the prop part of PROPFIND and REPORT bodies.
type propRequest struct {
	allprop  bool
	propname bool
	names    []xml.Name
}

// parsePropRequest reads <propfind> or a REPORT body: <prop>, <allprop/>
// (with <include>) or <propname/>; nothing means allprop.
func parsePropRequest(root *node) propRequest {
	if root == nil {
		return propRequest{allprop: true}
	}
	if root.child(dn("propname")) != nil {
		return propRequest{propname: true}
	}
	if p := root.child(dn("prop")); p != nil {
		pr := propRequest{}
		for _, k := range p.kids {
			pr.names = append(pr.names, k.name)
		}
		return pr
	}
	pr := propRequest{allprop: true}
	if inc := root.child(dn("include")); inc != nil {
		for _, k := range inc.kids {
			pr.names = append(pr.names, k.name)
		}
	}
	return pr
}

// Property names.
var (
	pResourceType        = dn("resourcetype")
	pDisplayName         = dn("displayname")
	pGetETag             = dn("getetag")
	pGetContentType      = dn("getcontenttype")
	pGetContentLength    = dn("getcontentlength")
	pGetLastModified     = dn("getlastmodified")
	pCurrentUserPrinc    = dn("current-user-principal")
	pPrincipalURL        = dn("principal-URL")
	pPrincipalCollSet    = dn("principal-collection-set")
	pOwner               = dn("owner")
	pCUPrivSet           = dn("current-user-privilege-set")
	pSupportedReportSet  = dn("supported-report-set")
	pSyncToken           = dn("sync-token")
	pQuotaUsed           = dn("quota-used-bytes")
	pQuotaAvailable      = dn("quota-available-bytes")
	pAlternateURISet     = dn("alternate-URI-set")
	pGroupMembership     = dn("group-membership")
	pCalHomeSet          = cn("calendar-home-set")
	pCalUserAddrSet      = cn("calendar-user-address-set")
	pCalUserType         = cn("calendar-user-type")
	pCalDescription      = cn("calendar-description")
	pSupportedCalComps   = cn("supported-calendar-component-set")
	pSupportedCalData    = cn("supported-calendar-data")
	pCalMaxResourceSize  = cn("max-resource-size")
	pCalData             = cn("calendar-data")
	pCalTimezone         = cn("calendar-timezone")
	pScheduleTransp      = cn("schedule-calendar-transp")
	pCardHomeSet         = crn("addressbook-home-set")
	pCardDescription     = crn("addressbook-description")
	pSupportedAddrData   = crn("supported-address-data")
	pCardMaxResourceSize = crn("max-resource-size")
	pAddressData         = crn("address-data")
	pGetCTag             = xml.Name{Space: nsCS, Local: "getctag"}
	pEmailAddressSet     = xml.Name{Space: nsCS, Local: "email-address-set"}
	pCalColor            = xml.Name{Space: nsApple, Local: "calendar-color"}
	pCalOrder            = xml.Name{Space: nsApple, Local: "calendar-order"}
)

// allprops are the properties allprop returns per kind of resource
// (RFC 4918 section 9.1: the live properties defined there, plus the
// cheap ones clients expect).
func allprops(res *resource) []xml.Name {
	switch res.typ {
	case resRoot:
		return []xml.Name{pResourceType, pCurrentUserPrinc}
	case resPrincipal:
		return []xml.Name{pResourceType, pDisplayName, pPrincipalURL, pCalHomeSet, pCardHomeSet, pCalUserAddrSet}
	case resHome:
		return []xml.Name{pResourceType}
	case resCollection:
		if res.kind == davobj.Calendar {
			return []xml.Name{pResourceType, pDisplayName, pCalDescription, pCalColor, pGetCTag, pSyncToken, pSupportedCalComps}
		}
		return []xml.Name{pResourceType, pDisplayName, pCardDescription, pGetCTag, pSyncToken}
	case resObject:
		return []xml.Name{pResourceType, pGetETag, pGetContentType, pGetContentLength, pGetLastModified}
	}
	return nil
}

// propCtx caches what several properties of one request need.
type propCtx struct {
	h      *Handler
	rq     *request
	usage  map[string]int64
	quotaE error
}

func (h *Handler) newPropCtx(rq *request) *propCtx {
	return &propCtx{h: h, rq: rq, usage: map[string]int64{}}
}

func (pc *propCtx) used(m *config.Mailbox) (int64, error) {
	if v, ok := pc.usage[m.StorageID]; ok {
		return v, nil
	}
	u, err := pc.rq.st.Usage(pc.rq.ctx, m.StorageID)
	if err != nil {
		return 0, err
	}
	pc.usage[m.StorageID] = u.Bytes
	return u.Bytes, nil
}

func privilegeSet(p Privileges) string {
	priv := func(name xml.Name) string { return elem(dn("privilege"), elem(name, "")) }
	var b strings.Builder
	if p.Has(PrivAll) {
		b.WriteString(priv(dn("all")))
	}
	if p.Has(PrivRead) {
		b.WriteString(priv(dn("read")) + priv(dn("read-current-user-privilege-set")) + priv(cn("read-free-busy")))
	}
	if p.Has(PrivWriteContent | PrivWriteProperties | PrivBind | PrivUnbind) {
		b.WriteString(priv(dn("write")))
	}
	if p.Has(PrivWriteContent) {
		b.WriteString(priv(dn("write-content")))
	}
	if p.Has(PrivWriteProperties) {
		b.WriteString(priv(dn("write-properties")))
	}
	if p.Has(PrivBind) {
		b.WriteString(priv(dn("bind")))
	}
	if p.Has(PrivUnbind) {
		b.WriteString(priv(dn("unbind")))
	}
	return b.String()
}

func reportSet(names ...xml.Name) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(elem(dn("supported-report"), elem(dn("report"), elem(n, ""))))
	}
	return b.String()
}

// userAddresses are the mailbox's own address and the aliases it may send
// as.
func userAddresses(rq *request, m *config.Mailbox) []string {
	out := []string{m.Address}
	for _, a := range rq.dir.Config().Aliases {
		if a.Address != m.Address && rq.dir.MaySendAs(m, a.Address) {
			out = append(out, a.Address)
		}
	}
	return out
}

const httpDate = "Mon, 02 Jan 2006 15:04:05 GMT"

// prop returns one property of a resource: its inner XML and status.
// data reads an object's data (calendar-data, address-data).
func (pc *propCtx) prop(res *resource, name xml.Name, data func() ([]byte, error)) (string, int, error) {
	rq := pc.rq
	switch name {
	case pCurrentUserPrinc:
		return href(principalPath(rq.user)), http.StatusOK, nil
	case pPrincipalCollSet:
		return href(Prefix + "principals/"), http.StatusOK, nil
	case pCUPrivSet:
		return privilegeSet(res.privs), http.StatusOK, nil
	}
	switch res.typ {
	case resRoot:
		switch name {
		case pResourceType:
			return elem(dn("collection"), ""), http.StatusOK, nil
		}
	case resPrincipal:
		m := res.owner
		switch name {
		case pResourceType:
			return elem(dn("principal"), ""), http.StatusOK, nil
		case pDisplayName:
			if m.DisplayName != "" {
				return escText(m.DisplayName), http.StatusOK, nil
			}
			return escText(m.Address), http.StatusOK, nil
		case pPrincipalURL:
			return href(principalPath(m)), http.StatusOK, nil
		case pCalHomeSet:
			return href(homePath(m, davobj.Calendar)), http.StatusOK, nil
		case pCardHomeSet:
			return href(homePath(m, davobj.AddressBook)), http.StatusOK, nil
		case pCalUserAddrSet, pAlternateURISet:
			var b strings.Builder
			for _, a := range userAddresses(rq, m) {
				b.WriteString(href("mailto:" + a))
			}
			if name == pCalUserAddrSet {
				b.WriteString(href(principalPath(m)))
			}
			return b.String(), http.StatusOK, nil
		case pEmailAddressSet:
			var b strings.Builder
			for _, a := range userAddresses(rq, m) {
				b.WriteString(elem(xml.Name{Space: nsCS, Local: "email-address"}, escText(a)))
			}
			return b.String(), http.StatusOK, nil
		case pCalUserType:
			return "INDIVIDUAL", http.StatusOK, nil
		case pGroupMembership:
			return "", http.StatusOK, nil
		case pSupportedReportSet:
			return reportSet(dn("expand-property"), dn("principal-property-search"), dn("principal-search-property-set")), http.StatusOK, nil
		}
	case resHome:
		switch name {
		case pResourceType:
			return elem(dn("collection"), ""), http.StatusOK, nil
		case pOwner:
			return href(principalPath(res.owner)), http.StatusOK, nil
		case pSupportedReportSet:
			return "", http.StatusOK, nil
		case pQuotaUsed, pQuotaAvailable:
			return pc.quota(res.owner, name)
		}
	case resCollection:
		if !res.exists {
			return "", http.StatusNotFound, nil
		}
		return pc.collectionProp(res, name)
	case resObject:
		if !res.exists {
			return "", http.StatusNotFound, nil
		}
		return pc.objectProp(res, name, data)
	}
	return "", http.StatusNotFound, nil
}

func (pc *propCtx) quota(m *config.Mailbox, name xml.Name) (string, int, error) {
	used, err := pc.used(m)
	if err != nil {
		return "", 0, err
	}
	if name == pQuotaUsed {
		return strconv.FormatInt(used, 10), http.StatusOK, nil
	}
	if m.QuotaBytes <= 0 {
		return "", http.StatusNotFound, nil
	}
	return strconv.FormatInt(max(0, m.QuotaBytes-used), 10), http.StatusOK, nil
}

func (pc *propCtx) collectionProp(res *resource, name xml.Name) (string, int, error) {
	cal := res.kind == davobj.Calendar
	switch name {
	case pResourceType:
		if cal {
			return elem(dn("collection"), "") + elem(cn("calendar"), ""), http.StatusOK, nil
		}
		return elem(dn("collection"), "") + elem(crn("addressbook"), ""), http.StatusOK, nil
	case pOwner:
		return href(principalPath(res.owner)), http.StatusOK, nil
	case pQuotaUsed, pQuotaAvailable:
		if res.dir != nil {
			return "", http.StatusNotFound, nil
		}
		return pc.quota(res.owner, name)
	}
	if res.dir != nil {
		switch name {
		case pDisplayName:
			return escText(directoryDisplayName), http.StatusOK, nil
		case pCardDescription:
			return escText(directoryDescription), http.StatusOK, nil
		case pGetCTag:
			return escText(res.dir.ctag), http.StatusOK, nil
		case pSyncToken:
			return escText(res.dir.token()), http.StatusOK, nil
		case pSupportedAddrData:
			return supportedAddressData, http.StatusOK, nil
		case pCardMaxResourceSize:
			return strconv.Itoa(davobj.MaxObjectBytes), http.StatusOK, nil
		case pSupportedReportSet:
			return reportSet(crn("addressbook-query"), crn("addressbook-multiget"), dn("sync-collection")), http.StatusOK, nil
		}
		return "", http.StatusNotFound, nil
	}
	c := res.coll
	switch name {
	case pDisplayName:
		if c.DisplayName == "" {
			return escText(c.Name), http.StatusOK, nil
		}
		return escText(c.DisplayName), http.StatusOK, nil
	case pGetCTag:
		return escText(c.CTag()), http.StatusOK, nil
	case pSyncToken:
		return escText(c.SyncToken()), http.StatusOK, nil
	}
	if cal {
		switch name {
		case pCalDescription:
			if c.Description == "" {
				return "", http.StatusNotFound, nil
			}
			return escText(c.Description), http.StatusOK, nil
		case pCalColor:
			if c.Color == "" {
				return "", http.StatusNotFound, nil
			}
			return escText(c.Color), http.StatusOK, nil
		case pSupportedCalComps:
			comps := c.Components
			if len(comps) == 0 {
				comps = []string{davobj.CompEvent, davobj.CompToDo}
			}
			var b strings.Builder
			for _, cmp := range comps {
				b.WriteString(`<c:comp name="` + escAttr(cmp) + `"/>`)
			}
			return b.String(), http.StatusOK, nil
		case pSupportedCalData:
			return `<c:calendar-data content-type="text/calendar" version="2.0"/>`, http.StatusOK, nil
		case pCalMaxResourceSize:
			return strconv.Itoa(davobj.MaxObjectBytes), http.StatusOK, nil
		case pSupportedReportSet:
			return reportSet(cn("calendar-query"), cn("calendar-multiget"), dn("sync-collection")), http.StatusOK, nil
		}
		return "", http.StatusNotFound, nil
	}
	switch name {
	case pCardDescription:
		if c.Description == "" {
			return "", http.StatusNotFound, nil
		}
		return escText(c.Description), http.StatusOK, nil
	case pSupportedAddrData:
		return supportedAddressData, http.StatusOK, nil
	case pCardMaxResourceSize:
		return strconv.Itoa(davobj.MaxObjectBytes), http.StatusOK, nil
	case pSupportedReportSet:
		return reportSet(crn("addressbook-query"), crn("addressbook-multiget"), dn("sync-collection")), http.StatusOK, nil
	}
	return "", http.StatusNotFound, nil
}

const supportedAddressData = `<card:address-data-type content-type="text/vcard" version="3.0"/><card:address-data-type content-type="text/vcard" version="4.0"/>`

// contentType is the media type of an object.
func contentType(kind, comp string) string {
	if kind == davobj.Calendar {
		return "text/calendar; charset=utf-8; component=" + strings.ToLower(comp)
	}
	return "text/vcard; charset=utf-8"
}

func (pc *propCtx) objectProp(res *resource, name xml.Name, data func() ([]byte, error)) (string, int, error) {
	etag, size, comp, mod := objectFacts(res)
	switch name {
	case pResourceType:
		return "", http.StatusOK, nil
	case pGetETag:
		return escText(`"` + etag + `"`), http.StatusOK, nil
	case pGetContentType:
		return escText(contentType(res.kind, comp)), http.StatusOK, nil
	case pGetContentLength:
		return strconv.FormatInt(size, 10), http.StatusOK, nil
	case pGetLastModified:
		return escText(mod.UTC().Format(httpDate)), http.StatusOK, nil
	case pOwner:
		return href(principalPath(res.owner)), http.StatusOK, nil
	case pCalData, pAddressData:
		if (name == pCalData) != (res.kind == davobj.Calendar) || data == nil {
			return "", http.StatusNotFound, nil
		}
		b, err := data()
		if errors.Is(err, storage.ErrNoObject) {
			return "", http.StatusNotFound, nil
		}
		if err != nil {
			return "", 0, err
		}
		return escText(string(b)), http.StatusOK, nil
	}
	return "", http.StatusNotFound, nil
}

// objectFacts are the ETag, size, component and modification time of an
// object of a collection or of the directory.
func objectFacts(res *resource) (etag string, size int64, comp string, mod time.Time) {
	if res.card != nil {
		return res.card.etag, int64(len(res.card.data)), davobj.CompCard, res.card.modified
	}
	return res.obj.ETag(), res.obj.Size, res.obj.Component, res.obj.Modified
}

// objectData returns a function reading the data of an object resource.
func (pc *propCtx) objectData(res *resource) func() ([]byte, error) {
	return func() ([]byte, error) {
		if res.card != nil {
			return res.card.data, nil
		}
		return pc.rq.st.ReadDAVObject(*res.obj)
	}
}

// props computes the requested properties of a resource.
func (pc *propCtx) props(res *resource, pr propRequest) ([]propResult, error) {
	var names []xml.Name
	switch {
	case pr.propname:
		var out []propResult
		for _, n := range allprops(res) {
			out = append(out, propResult{name: n, status: http.StatusOK})
		}
		return out, nil
	case pr.allprop:
		names = append(allprops(res), pr.names...)
	default:
		names = pr.names
	}
	var data func() ([]byte, error)
	if res.typ == resObject && res.exists {
		data = pc.objectData(res)
	}
	out := make([]propResult, 0, len(names))
	seen := map[xml.Name]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		inner, st, err := pc.prop(res, n, data)
		if err != nil {
			return nil, err
		}
		if pr.allprop && st == http.StatusNotFound && !containsName(pr.names, n) {
			continue
		}
		out = append(out, propResult{name: n, inner: inner, status: st})
	}
	return out, nil
}

func containsName(list []xml.Name, n xml.Name) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// ---- PROPFIND ----

func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ == resNone || !res.exists {
		http.NotFound(w, r)
		return
	}
	depth := r.Header.Get("Depth")
	switch depth {
	case "0", "1":
	case "", "infinity":
		if res.typ == resObject {
			depth = "0"
			break
		}
		writeError(w, http.StatusForbidden, dn("propfind-finite-depth"), "")
		return
	default:
		http.Error(w, "The Depth header must be 0 or 1.", http.StatusBadRequest)
		return
	}
	root, ok := readXML(w, r)
	if !ok {
		return
	}
	if root != nil && root.name != dn("propfind") {
		http.Error(w, "Expected a propfind element.", http.StatusBadRequest)
		return
	}
	pr := parsePropRequest(root)
	pc := h.newPropCtx(rq)
	ms := newMS(w)
	if err := h.propfindOne(w, ms, pc, res, pr); err != nil {
		h.fail(w, err)
		return
	}
	if depth == "1" {
		if err := h.propfindChildren(w, ms, pc, res, pr); err != nil {
			if !ms.started {
				h.fail(w, err)
			} else {
				h.log.Error("mail: calendar or contacts listing failed", "err", err)
			}
			return
		}
	}
	ms.end(w, "")
}

func (h *Handler) propfindOne(w http.ResponseWriter, ms *msWriter, pc *propCtx, res *resource, pr propRequest) error {
	props, err := pc.props(res, pr)
	if err != nil {
		return err
	}
	ms.response(w, res.path, props)
	return nil
}

// propfindChildren lists the members of a home or collection.
func (h *Handler) propfindChildren(w http.ResponseWriter, ms *msWriter, pc *propCtx, res *resource, pr propRequest) error {
	rq := pc.rq
	switch res.typ {
	case resHome:
		colls, err := rq.st.DAVCollections(rq.ctx, res.owner.StorageID, res.kind)
		if err != nil {
			return err
		}
		for i := range colls {
			c := &colls[i]
			privs := h.opts.Authorizer.Collection(rq.dir, rq.user, res.owner, res.kind, c.Name)
			if privs == PrivNone {
				continue
			}
			child := &resource{typ: resCollection, path: collectionPath(res.owner, res.kind, c.Name), owner: res.owner, kind: res.kind, privs: privs, coll: c, name: c.Name, exists: true}
			if err := h.propfindOne(w, ms, pc, child, pr); err != nil {
				return err
			}
		}
		if res.kind == davobj.AddressBook && res.owner.StorageID == rq.user.StorageID {
			child := &resource{typ: resCollection, path: collectionPath(res.owner, res.kind, directoryName), owner: res.owner, kind: res.kind, privs: PrivRead,
				dir: h.directoryBook(rq.dir, rq.user), name: directoryName, exists: true}
			if err := h.propfindOne(w, ms, pc, child, pr); err != nil {
				return err
			}
		}
	case resCollection:
		return h.eachObject(rq, res, nil, func(child *resource) error {
			return h.propfindOne(w, ms, pc, child, pr)
		})
	}
	return nil
}

// eachObject calls fn for the objects of a collection (only those listed
// in objs when not nil).
func (h *Handler) eachObject(rq *request, res *resource, objs []storage.DAVObject, fn0 func(*resource) error) error {
	// Stop as soon as the client is gone.
	fn := func(r *resource) error {
		if err := rq.ctx.Err(); err != nil {
			return err
		}
		return fn0(r)
	}
	if res.dir != nil {
		for _, card := range res.dir.cards {
			child := &resource{typ: resObject, path: objectPath(res.owner, res.kind, directoryName, card.name), owner: res.owner, kind: res.kind,
				privs: res.privs, dir: res.dir, name: card.name, card: card, exists: true}
			if err := fn(child); err != nil {
				return err
			}
		}
		return nil
	}
	if objs == nil {
		var err error
		objs, err = rq.st.DAVObjects(rq.ctx, *res.coll, 0, 0)
		if err != nil {
			return err
		}
	}
	for i := range objs {
		o := &objs[i]
		child := &resource{typ: resObject, path: objectPath(res.owner, res.kind, res.coll.Name, o.Name), owner: res.owner, kind: res.kind,
			privs: res.privs, coll: res.coll, name: o.Name, obj: o, exists: true}
		if err := fn(child); err != nil {
			return err
		}
	}
	return nil
}

// ---- PROPPATCH ----

var colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?$`)

const (
	maxDisplayName = 200
	maxDescription = 2000
)

func oneLine(s string, max int) bool {
	return utf8.RuneCountInString(s) <= max && !strings.ContainsAny(s, "\r\n\x00")
}

// ignoredProps are accepted by PROPPATCH but not kept: programs set them
// and fail when refused, and nothing here depends on them.
var ignoredProps = map[xml.Name]bool{pCalOrder: true, pCalTimezone: true, pScheduleTransp: true}

func (h *Handler) proppatch(w http.ResponseWriter, r *http.Request, rq *request, res *resource) {
	if res.typ == resNone || !res.exists {
		http.NotFound(w, r)
		return
	}
	root, ok := readXML(w, r)
	if !ok {
		return
	}
	if root == nil || root.name != dn("propertyupdate") {
		http.Error(w, "Expected a propertyupdate element.", http.StatusBadRequest)
		return
	}
	type change struct {
		name  xml.Name
		value string
		set   bool
	}
	var changes []change
	for _, op := range root.kids {
		set := op.name == dn("set")
		if !set && op.name != dn("remove") {
			continue
		}
		for _, p := range op.children(dn("prop")) {
			for _, k := range p.kids {
				changes = append(changes, change{name: k.name, value: strings.TrimSpace(k.text.String()), set: set})
			}
		}
	}
	writable := res.typ == resCollection && res.coll != nil && res.privs.Has(PrivWriteProperties)
	var u storage.DAVCollectionUpdate
	results := make([]propResult, len(changes))
	failed := false
	for i, ch := range changes {
		results[i] = propResult{name: ch.name, status: http.StatusOK}
		if !writable {
			results[i].status = http.StatusForbidden
			failed = true
			continue
		}
		v := ch.value
		if !ch.set {
			v = ""
		}
		okValue := true
		switch {
		case ch.name == pDisplayName:
			okValue = oneLine(v, maxDisplayName)
			u.DisplayName = &v
		case ch.name == pCalDescription && res.kind == davobj.Calendar, ch.name == pCardDescription && res.kind == davobj.AddressBook:
			okValue = utf8.RuneCountInString(v) <= maxDescription && !strings.ContainsRune(v, 0)
			u.Description = &v
		case ch.name == pCalColor && res.kind == davobj.Calendar:
			okValue = v == "" || colorRe.MatchString(v)
			u.Color = &v
		case ignoredProps[ch.name] && res.kind == davobj.Calendar:
		default:
			okValue = false
		}
		if !okValue {
			results[i].status = http.StatusForbidden
			failed = true
		}
	}
	if failed {
		for i := range results {
			if results[i].status == http.StatusOK {
				results[i].status = http.StatusFailedDependency
			}
		}
	} else if writable {
		if err := rq.st.UpdateDAVCollection(rq.ctx, res.owner.StorageID, res.coll.ID, u); err != nil {
			h.fail(w, err)
			return
		}
	}
	ms := newMS(w)
	ms.response(w, res.path, results)
	ms.end(w, "")
}
