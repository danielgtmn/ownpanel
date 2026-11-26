package sieve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limits of one run.
const (
	// DefaultMaxSteps bounds the commands, tests and comparisons of one run.
	DefaultMaxSteps = 50_000
	// DefaultTimeout bounds one run (when the context has no earlier
	// deadline).
	DefaultTimeout = 2 * time.Second
	// maxIncludeDepth bounds nested includes.
	maxIncludeDepth = 5
	// maxVariableBytes bounds the value of one variable.
	maxVariableBytes = 16 << 10
	// maxVariables bounds the variables of one run.
	maxVariables = 512
	// maxActions bounds the actions of one run.
	maxActions = 100
	// MinVacationPeriod and MaxVacationPeriod bound how often a sender
	// gets an out-of-office reply; DefaultVacationPeriod applies without
	// :days or :seconds.
	MinVacationPeriod     = 24 * time.Hour
	DefaultVacationPeriod = 7 * 24 * time.Hour
	MaxVacationPeriod     = 90 * 24 * time.Hour
	// DefaultDuplicatePeriod and MaxDuplicatePeriod are for "duplicate".
	DefaultDuplicatePeriod = 7 * 24 * time.Hour
	MaxDuplicatePeriod     = 30 * 24 * time.Hour
)

// Errors of a run.
var (
	ErrSteps   = errors.New("the filter took too many steps")
	ErrTimeout = errors.New("the filter took too long")
)

// Message is what a run may read of the message.
type Message interface {
	// Header returns the values of a header field (name in lowercase), in
	// order, unfolded and without the name, as in the message (not
	// decoded).
	Header(name string) []string
	// Size is the size of the message in bytes.
	Size() int64
	// Body returns the parts of the body for the body test: the whole body
	// undecoded ("raw"), the decoded text parts ("text"), or the decoded
	// parts whose type matches one of types ("content"; "" matches all, a
	// main type like "text" every subtype). The caller bounds the size.
	Body(transform string, types []string) []string
}

// Env is everything a run sees besides the script.
type Env struct {
	Message Message
	// EnvelopeFrom is the envelope sender ("" for the null sender),
	// EnvelopeTo the recipient as the sending server gave it.
	EnvelopeFrom string
	EnvelopeTo   string
	// Now is the time of delivery; zero means time.Now. Location is the
	// server's zone for dates without :zone; nil means UTC.
	Now      time.Time
	Location *time.Location
	// SpamTest is the spamtest value "0" to "10" ("0": not tested) and
	// SpamPercent "0" to "100" for :percent.
	SpamTest    string
	SpamPercent string
	// FolderExists tells whether a folder exists (mailboxexists); nil:
	// none does.
	FolderExists func(name string) bool
	// Duplicate tells whether id was seen and is still remembered; it must
	// not record anything (the caller records Result.Duplicates once the
	// message is stored, RFC 7352). nil: never.
	Duplicate func(id string) bool
	// Include returns another script of the mailbox by name; ok false when
	// there is none. ctx is the run's (it ends with the run's time limit);
	// each name is asked for at most once per run.
	Include func(ctx context.Context, name string) (s *Script, ok bool, err error)
	// CharsetReader decodes header words in other charsets than UTF-8
	// and US-ASCII; nil leaves them encoded.
	CharsetReader func(charset string, input io.Reader) (io.Reader, error)
	// MaxSteps bounds the run; 0 means DefaultMaxSteps.
	MaxSteps int
}

// ActionKind is what an action does.
type ActionKind int

const (
	ActKeep ActionKind = iota
	ActFileinto
	ActRedirect
	ActDiscard
	ActVacation
)

// Action is one thing a run decided.
type Action struct {
	Kind ActionKind
	// Folder for fileinto; Create: create it when missing (mailbox).
	Folder string
	Create bool
	// Flags for keep and fileinto; HasFlags tells whether they were set
	// (imap4flags), even to none.
	Flags    []string
	HasFlags bool
	// Address for redirect.
	Address string
	// Copy: the action does not cancel the implicit keep (fileinto and
	// redirect with :copy).
	Copy     bool
	Vacation *Vacation
}

// Vacation is an out-of-office reply to send (RFC 5230); the caller
// decides whether it may be sent.
type Vacation struct {
	// Period between two replies to the same sender (already bounded by
	// MinVacationPeriod and MaxVacationPeriod).
	Period  time.Duration
	Subject string // "" means none was given
	From    string // "" means the mailbox's address
	// Addresses are more addresses of the person (besides the mailbox's
	// own, which the caller adds).
	Addresses []string
	MIME      bool
	Handle    string
	Reason    string
}

// DuplicateMark is an id the duplicate test looked at: the caller records
// it once the message was delivered, for Period; Last moves the time of an
// id seen before (RFC 7352 :last).
type DuplicateMark struct {
	ID     string
	Period time.Duration
	Last   bool
}

// Result is the outcome of a run.
type Result struct {
	Actions []Action
	// Duplicates are to be recorded once the message is stored.
	Duplicates []DuplicateMark
	// ImplicitKeep: no action cancelled the implicit keep; it stores the
	// message with KeepFlags (when HasFlags).
	ImplicitKeep bool
	KeepFlags    []string
	HasFlags     bool
}

// Run executes the script. On an error the caller keeps the message as if
// there were no script (RFC 5228 2.10.6) and ignores the result.
func (s *Script) Run(ctx context.Context, env *Env) (*Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	if env.Now.IsZero() {
		env.Now = time.Now()
	}
	if env.Location == nil {
		env.Location = time.UTC
	}
	r := &runner{
		ctx: ctx, env: env, max: env.MaxSteps,
		globals:  map[string]string{},
		included: map[string]bool{},
		res:      &Result{ImplicitKeep: true},
		regexes:  map[string]*regexCache{},
		includes: map[string]includeResult{},
	}
	if r.max <= 0 {
		r.max = DefaultMaxSteps
	}
	r.dec = &mime.WordDecoder{CharsetReader: env.CharsetReader}
	top := &scope{script: s, vars: map[string]string{}, global: map[string]bool{}}
	err := r.exec(top, s.cmds)
	if errors.Is(err, errStop) || errors.Is(err, errReturn) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if s.exts["imap4flags"] || r.flagsUsed {
		r.res.KeepFlags, r.res.HasFlags = slices.Clone(r.flags), true
	}
	return r.res, nil
}

var (
	errStop   = errors.New("stop")
	errReturn = errors.New("return")
)

type scope struct {
	script *Script
	vars   map[string]string
	global map[string]bool // names declared global in this script
}

type includeResult struct {
	s   *Script
	ok  bool
	err error
}

type regexCache struct {
	re    *regexp.Regexp
	insts int
	err   error
}

type runner struct {
	ctx   context.Context
	env   *Env
	steps int
	max   int
	dec   *mime.WordDecoder

	globals   map[string]string
	nvars     int
	matchVars []string // ${0} … ${9}
	flags     []string // internal flags variable of imap4flags
	included  map[string]bool
	stack     []string // names of the scripts being included
	vacation  bool
	// flagsUsed: some script of the run uses imap4flags.
	flagsUsed bool
	res       *Result
	regexes   map[string]*regexCache
	includes  map[string]includeResult
}

func (r *runner) step(n int) error {
	r.steps += n
	if r.steps > r.max {
		return ErrSteps
	}
	if r.ctx.Err() != nil {
		return ErrTimeout
	}
	return nil
}

func (r *runner) fail(c interface{ line() int }, format string, args ...any) error {
	return errorAt(c.line(), format, args...)
}

func (r *runner) exec(sc *scope, cmds []command) error {
	if sc.script.exts["imap4flags"] {
		r.flagsUsed = true
	}
	for _, c := range cmds {
		if err := r.step(1); err != nil {
			return err
		}
		if err := r.execOne(sc, c); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) addAction(c command, a Action) error {
	if len(r.res.Actions) >= maxActions {
		return r.fail(c, "too many actions")
	}
	r.res.Actions = append(r.res.Actions, a)
	return nil
}

func (r *runner) execOne(sc *scope, c command) error {
	switch c := c.(type) {
	case *cmdIf:
		for i, t := range c.conds {
			ok, err := r.test(sc, t)
			if err != nil {
				return err
			}
			if ok {
				return r.exec(sc, c.blocks[i])
			}
		}
		if c.orElse != nil {
			return r.exec(sc, c.orElse)
		}
		return nil
	case *cmdStop:
		return errStop
	case *cmdReturn:
		return errReturn
	case *cmdDiscard:
		r.res.ImplicitKeep = false
		return r.addAction(c, Action{Kind: ActDiscard})
	case *cmdKeep:
		r.res.ImplicitKeep = false
		a := Action{Kind: ActKeep}
		a.Flags, a.HasFlags = r.actionFlags(sc, c.flags, c.hasFlags)
		return r.addAction(c, a)
	case *cmdFileinto:
		folder := r.expand(sc, c.folder)
		if folder == "" {
			return r.fail(c, "fileinto needs a folder name")
		}
		if !c.copy {
			r.res.ImplicitKeep = false
		}
		a := Action{Kind: ActFileinto, Folder: folder, Create: c.create, Copy: c.copy}
		a.Flags, a.HasFlags = r.actionFlags(sc, c.flags, c.hasFlags)
		return r.addAction(c, a)
	case *cmdRedirect:
		addr := strings.TrimSpace(r.expand(sc, c.addr))
		if !validRedirect(addr) {
			return r.fail(c, "redirect needs one e-mail address, found %q", addr)
		}
		if !c.copy {
			r.res.ImplicitKeep = false
		}
		return r.addAction(c, Action{Kind: ActRedirect, Address: addr, Copy: c.copy})
	case *cmdSet:
		v := r.expand(sc, c.value)
		for _, m := range c.mods {
			v = applyModifier(m, v)
		}
		return r.setVar(sc, c, c.name, v)
	case *cmdFlags:
		cur := r.flags
		if c.varname != "" {
			cur = strings.Fields(r.getVar(sc, c.varname))
		}
		var list []string
		for _, f := range c.flags {
			list = append(list, strings.Fields(r.expand(sc, f))...)
		}
		switch c.op {
		case flagSet:
			cur = uniqueFlags(list)
		case flagAdd:
			cur = uniqueFlags(append(slices.Clone(cur), list...))
		case flagRemove:
			cur = slices.DeleteFunc(slices.Clone(cur), func(f string) bool {
				return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, f) })
			})
		}
		if c.varname != "" {
			return r.setVar(sc, c, c.varname, strings.Join(cur, " "))
		}
		r.flags = cur
		return nil
	case *cmdVacation:
		if r.vacation {
			return r.fail(c, "only one out-of-office reply per message")
		}
		r.vacation = true
		v := &Vacation{
			Period: DefaultVacationPeriod,
			Reason: r.expand(sc, c.reason),
			MIME:   c.mime,
		}
		if c.hasPeriod {
			v.Period = time.Duration(c.period) * time.Second
		}
		v.Period = min(max(v.Period, MinVacationPeriod), MaxVacationPeriod)
		if c.hasSubject {
			v.Subject = r.expand(sc, c.subject)
		}
		if c.hasFrom {
			v.From = r.expand(sc, c.from)
		}
		for _, a := range c.addresses {
			v.Addresses = append(v.Addresses, r.expand(sc, a))
		}
		if c.hasHandle {
			v.Handle = r.expand(sc, c.handle)
		} else {
			// RFC 5230 4.2: the same reply is the same handle.
			h := sha256.Sum256([]byte(v.Subject + "\x00" + v.From + "\x00" + v.Reason + "\x00" + strconv.FormatBool(v.MIME)))
			v.Handle = "auto:" + hex.EncodeToString(h[:12])
		}
		return r.addAction(c, Action{Kind: ActVacation, Vacation: v})
	case *cmdInclude:
		return r.include(sc, c)
	case *cmdGlobal:
		for _, n := range c.names {
			if v, ok := sc.vars[n]; ok {
				if _, exists := r.globals[n]; !exists {
					r.globals[n] = v
				}
				delete(sc.vars, n)
			}
			sc.global[n] = true
		}
		return nil
	}
	return r.fail(c, "unknown command")
}

func (r *runner) include(sc *scope, c *cmdInclude) error {
	name := c.name
	if c.once && r.included[name] {
		return nil
	}
	if slices.Contains(r.stack, name) {
		return r.fail(c, "script %q includes itself", name)
	}
	if len(r.stack) >= maxIncludeDepth {
		return r.fail(c, "includes nested deeper than %d", maxIncludeDepth)
	}
	if r.env.Include == nil {
		if c.optional {
			return nil
		}
		return r.fail(c, "script %q not found", name)
	}
	inc, cached := r.includes[name]
	if !cached {
		inc.s, inc.ok, inc.err = r.env.Include(r.ctx, name)
		r.includes[name] = inc
	}
	s, ok, err := inc.s, inc.ok, inc.err
	if err != nil {
		return r.fail(c, "script %q: %v", name, err)
	}
	if !ok {
		if c.optional {
			return nil
		}
		return r.fail(c, "script %q not found", name)
	}
	r.included[name] = true
	r.stack = append(r.stack, name)
	err = r.exec(&scope{script: s, vars: map[string]string{}, global: map[string]bool{}}, s.cmds)
	r.stack = r.stack[:len(r.stack)-1]
	if errors.Is(err, errReturn) {
		return nil
	}
	return err
}

// actionFlags are the flags an action stores the message with.
func (r *runner) actionFlags(sc *scope, list []string, has bool) ([]string, bool) {
	if !sc.script.exts["imap4flags"] {
		return nil, false
	}
	if !has {
		return slices.Clone(r.flags), true
	}
	var out []string
	for _, f := range list {
		out = append(out, strings.Fields(r.expand(sc, f))...)
	}
	return uniqueFlags(out), true
}

func uniqueFlags(in []string) []string {
	out := []string{}
	for _, f := range in {
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, f) }) {
			out = append(out, f)
		}
	}
	return out
}

// Variables.

func (r *runner) getVar(sc *scope, name string) string {
	if sc.global[name] {
		return r.globals[name]
	}
	return sc.vars[name]
}

func (r *runner) setVar(sc *scope, c command, name, v string) error {
	if len(v) > maxVariableBytes {
		v = truncateUTF8(v, maxVariableBytes)
	}
	target := sc.vars
	if sc.global[name] {
		target = r.globals
	}
	if _, ok := target[name]; !ok {
		r.nvars++
		if r.nvars > maxVariables {
			return r.fail(c, "too many variables")
		}
	}
	target[name] = v
	return nil
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// expand fills in ${…} references when the script uses variables.
func (r *runner) expand(sc *scope, s string) string {
	if !sc.script.exts["variables"] || !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			break
		}
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			b.WriteString(s)
			break
		}
		name := s[i+2 : i+end]
		b.WriteString(s[:i])
		if v, ok := r.lookup(sc, name); ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+end+1])
		}
		s = s[i+end+1:]
		if b.Len() > maxVariableBytes*4 {
			break
		}
	}
	return b.String()
}

// lookup resolves the name inside ${…}; ok is false when it is not a
// variable reference at all (then it stays as written).
func (r *runner) lookup(sc *scope, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if name[0] >= '0' && name[0] <= '9' {
		n, err := strconv.Atoi(name)
		if err != nil {
			return "", false
		}
		if n < len(r.matchVars) {
			return r.matchVars[n], true
		}
		return "", true
	}
	lname := strings.ToLower(name)
	if !validVarName(lname) {
		return "", false
	}
	return r.getVar(sc, lname), true
}

func applyModifier(m, v string) string {
	switch m {
	case "lower":
		return asciiLower(v)
	case "upper":
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return r - 32
			}
			return r
		}, v)
	case "lowerfirst":
		if v != "" && v[0] >= 'A' && v[0] <= 'Z' {
			return string(v[0]+32) + v[1:]
		}
	case "upperfirst":
		if v != "" && v[0] >= 'a' && v[0] <= 'z' {
			return string(v[0]-32) + v[1:]
		}
	case "quotewildcard":
		var b strings.Builder
		for _, c := range v {
			if c == '*' || c == '?' || c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(c)
		}
		return b.String()
	case "quoteregex":
		return quoteMeta(v)
	case "length":
		return strconv.Itoa(len([]rune(v)))
	}
	return v
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 32
				}
			}
			return string(b)
		}
	}
	return s
}

// Tests.

func (r *runner) test(sc *scope, t test) (bool, error) {
	if err := r.step(1); err != nil {
		return false, err
	}
	switch t := t.(type) {
	case *testConst:
		return t.v, nil
	case *testNot:
		ok, err := r.test(sc, t.t)
		return !ok, err
	case *testAllOf:
		for _, sub := range t.tests {
			ok, err := r.test(sc, sub)
			if err != nil {
				return false, err
			}
			if ok == t.any {
				return ok, nil
			}
		}
		return !t.any, nil
	case *testExists:
		for _, h := range t.headers {
			if len(r.env.Message.Header(strings.ToLower(r.expand(sc, h)))) == 0 {
				return false, nil
			}
		}
		return true, nil
	case *testSize:
		if t.over {
			return r.env.Message.Size() > t.n, nil
		}
		return r.env.Message.Size() < t.n, nil
	case *testHeader:
		var values []string
		for _, h := range t.headers {
			for _, v := range r.env.Message.Header(strings.ToLower(r.expand(sc, h))) {
				values = append(values, r.decodeHeader(v))
			}
		}
		return r.match(sc, t.m, values, t.keys)
	case *testAddress:
		var values []string
		for _, h := range t.headers {
			h = strings.ToLower(r.expand(sc, h))
			if t.envelope {
				var a string
				switch h {
				case "from":
					a = r.env.EnvelopeFrom
				case "to":
					a = r.env.EnvelopeTo
				default:
					continue
				}
				values = append(values, addressPart(a, t.part))
				continue
			}
			for _, v := range r.env.Message.Header(h) {
				for _, a := range parseAddresses(v) {
					values = append(values, addressPart(a, t.part))
				}
			}
		}
		return r.match(sc, t.m, values, t.keys)
	case *testBody:
		var types []string
		for _, ty := range t.types {
			types = append(types, asciiLower(r.expand(sc, ty)))
		}
		return r.match(sc, t.m, r.env.Message.Body(t.transform, types), t.keys)
	case *testDate:
		return r.date(sc, t)
	case *testString:
		var values []string
		for _, s := range t.sources {
			values = append(values, r.expand(sc, s))
		}
		if t.m.kind == "count" {
			// RFC 5229 5: empty strings do not count.
			values = slices.DeleteFunc(values, func(s string) bool { return s == "" })
		}
		return r.match(sc, t.m, values, t.keys)
	case *testHasFlag:
		var flags []string
		if len(t.vars) == 0 {
			flags = r.flags
		} else {
			for _, v := range t.vars {
				flags = append(flags, strings.Fields(r.getVar(sc, strings.ToLower(r.expand(sc, v))))...)
			}
		}
		return r.match(sc, t.m, flags, t.keys)
	case *testMailboxExists:
		for _, n := range t.names {
			if r.env.FolderExists == nil || !r.env.FolderExists(r.expand(sc, n)) {
				return false, nil
			}
		}
		return true, nil
	case *testDuplicate:
		return r.duplicate(sc, t), nil
	case *testSpamTest:
		v := r.env.SpamTest
		if t.percent {
			v = r.env.SpamPercent
		}
		if v == "" {
			v = "0"
		}
		return r.match(sc, t.m, []string{v}, []string{t.value})
	}
	return false, r.fail(t, "unknown test")
}

func (r *runner) decodeHeader(v string) string {
	if !strings.Contains(v, "=?") {
		return v
	}
	d, err := r.dec.DecodeHeader(v)
	if err != nil {
		return v
	}
	return d
}

func (r *runner) duplicate(sc *scope, t *testDuplicate) bool {
	var id string
	if t.hasUniqueID {
		id = r.expand(sc, t.uniqueID)
	} else {
		vs := r.env.Message.Header(t.header)
		if len(vs) == 0 {
			return false
		}
		id = strings.TrimSpace(vs[0])
	}
	if id == "" || r.env.Duplicate == nil {
		return false
	}
	period := DefaultDuplicatePeriod
	if t.hasSeconds {
		period = time.Duration(t.seconds) * time.Second
	}
	period = min(period, MaxDuplicatePeriod)
	h := sha256.Sum256([]byte(r.expand(sc, t.handle) + "\x00" + t.header + "\x00" + id))
	key := hex.EncodeToString(h[:16])
	if len(r.res.Duplicates) < maxActions {
		r.res.Duplicates = append(r.res.Duplicates, DuplicateMark{ID: key, Period: period, Last: t.last})
	}
	return r.env.Duplicate(key)
}

// parseAddresses splits a header value into addresses (local@domain).
// What cannot be parsed counts as one address as written.
func parseAddresses(v string) []string {
	list, err := mail.ParseAddressList(v)
	if err == nil {
		out := make([]string, 0, len(list))
		for _, a := range list {
			out = append(out, a.Address)
		}
		return out
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i, j := strings.LastIndexByte(part, '<'), strings.LastIndexByte(part, '>'); i >= 0 && j > i {
			part = part[i+1 : j]
		}
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func addressPart(a, part string) string {
	switch part {
	case "localpart":
		if i := strings.LastIndexByte(a, '@'); i >= 0 {
			return a[:i]
		}
		return a
	case "domain":
		if i := strings.LastIndexByte(a, '@'); i >= 0 {
			return a[i+1:]
		}
		return ""
	}
	return a
}

// Dates (RFC 5260).

var dateParts = []string{"year", "month", "day", "date", "julian", "hour", "minute", "second", "time", "iso8601", "std11", "zone", "weekday"}

// parseZone reads "+hhmm" / "-hhmm".
func parseZone(z string) (*time.Location, bool) {
	if len(z) != 5 || (z[0] != '+' && z[0] != '-') {
		return nil, false
	}
	h, err1 := strconv.Atoi(z[1:3])
	m, err2 := strconv.Atoi(z[3:5])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return nil, false
	}
	off := h*3600 + m*60
	if z[0] == '-' {
		off = -off
	}
	return time.FixedZone(z, off), true
}

func datePart(t time.Time, part string) string {
	switch part {
	case "year":
		return fmt.Sprintf("%04d", t.Year())
	case "month":
		return fmt.Sprintf("%02d", int(t.Month()))
	case "day":
		return fmt.Sprintf("%02d", t.Day())
	case "date":
		return t.Format("2006-01-02")
	case "julian":
		// Modified Julian Day: days since 1858-11-17.
		y, m, d := t.Date()
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return strconv.FormatInt(int64(day.Sub(time.Date(1858, 11, 17, 0, 0, 0, 0, time.UTC))/(24*time.Hour)), 10)
	case "hour":
		return fmt.Sprintf("%02d", t.Hour())
	case "minute":
		return fmt.Sprintf("%02d", t.Minute())
	case "second":
		return fmt.Sprintf("%02d", t.Second())
	case "time":
		return t.Format("15:04:05")
	case "iso8601":
		return t.Format("2006-01-02T15:04:05-07:00")
	case "std11":
		return t.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	case "zone":
		return t.Format("-0700")
	case "weekday":
		return strconv.Itoa(int(t.Weekday()))
	}
	return ""
}

func (r *runner) date(sc *scope, t *testDate) (bool, error) {
	var when time.Time
	if t.current {
		when = r.env.Now
	} else {
		vs := r.env.Message.Header(strings.ToLower(r.expand(sc, t.header)))
		if len(vs) == 0 {
			return false, nil
		}
		v := vs[0]
		// The Received field has the date after the last ";".
		if i := strings.LastIndexByte(v, ';'); i >= 0 && strings.EqualFold(t.header, "received") {
			v = v[i+1:]
		}
		d, err := mail.ParseDate(strings.TrimSpace(v))
		if err != nil {
			return false, nil
		}
		when = d
	}
	switch {
	case t.hasZone:
		loc, ok := parseZone(r.expand(sc, t.zone))
		if !ok {
			return false, r.fail(t, "%q is not a time zone offset like +0100", t.zone)
		}
		when = when.In(loc)
	case t.original && !t.current:
		// Keep the zone the date was written in.
	default:
		when = when.In(r.env.Location)
	}
	part := strings.ToLower(r.expand(sc, t.part))
	if !slices.Contains(dateParts, part) {
		return false, r.fail(t, "%q is not a date part", part)
	}
	return r.match(sc, t.m, []string{datePart(when, part)}, t.keys)
}
