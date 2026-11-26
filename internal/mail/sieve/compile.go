package sieve

import (
	"slices"
	"strings"
)

// Extensions lists the capabilities offered (for "require" and the
// ManageSieve SIEVE capability), in the order they are announced.
var Extensions = []string{
	"fileinto", "mailbox", "envelope", "imap4flags", "body", "copy",
	"variables", "relational", "date", "vacation", "vacation-seconds",
	"include", "duplicate", "spamtest", "spamtestplus", "regex",
	"comparator-i;ascii-numeric", "comparator-i;octet", "comparator-i;ascii-casemap",
}

// MaxRegexBytes bounds a regular expression (after variables are filled in).
const MaxRegexBytes = 1024

// MaxScripts bounds the scripts of one mailbox.
const MaxScripts = 10

// Script is a checked script, ready to run. It is safe for concurrent use.
type Script struct {
	exts map[string]bool
	cmds []command
}

// Requires tells whether the script required an extension.
func (s *Script) Requires(ext string) bool { return s.exts[ext] }

// Compile parses and checks a script.
func Compile(src string) (*Script, error) {
	nodes, err := parse(src)
	if err != nil {
		return nil, err
	}
	c := &compiler{exts: map[string]bool{}}
	cmds, err := c.block(nodes, true)
	if err != nil {
		return nil, err
	}
	return &Script{exts: c.exts, cmds: cmds}, nil
}

type compiler struct {
	exts map[string]bool
}

// Commands.

type command interface{ line() int }

type pos struct{ ln int }

func (p pos) line() int { return p.ln }

type cmdIf struct {
	pos
	conds  []test
	blocks [][]command
	// orElse runs when no condition held (nil: none).
	orElse []command
}

type cmdStop struct{ pos }
type cmdReturn struct{ pos }
type cmdDiscard struct{ pos }

type cmdKeep struct {
	pos
	flags    []string
	hasFlags bool
}

type cmdFileinto struct {
	pos
	folder   string
	copy     bool
	create   bool
	flags    []string
	hasFlags bool
}

type cmdRedirect struct {
	pos
	addr string
	copy bool
}

type cmdSet struct {
	pos
	name  string
	value string
	mods  []string // in the order they apply
}

const (
	flagSet = iota
	flagAdd
	flagRemove
)

type cmdFlags struct {
	pos
	op      int
	varname string // "" means the internal variable
	flags   []string
}

type cmdVacation struct {
	pos
	period     int64 // seconds; 0: default
	hasPeriod  bool
	subject    string
	hasSubject bool
	from       string
	hasFrom    bool
	addresses  []string
	mime       bool
	handle     string
	hasHandle  bool
	reason     string
}

type cmdInclude struct {
	pos
	name     string
	once     bool
	optional bool
}

type cmdGlobal struct {
	pos
	names []string
}

// Tests.

type test interface{ line() int }

type matcher struct {
	comparator string // "i;octet", "i;ascii-casemap", "i;ascii-numeric"
	kind       string // "is", "contains", "matches", "regex", "value", "count"
	rel        string // for value and count: gt ge lt le eq ne
}

type testAddress struct {
	pos
	m        matcher
	part     string // all, localpart, domain
	envelope bool
	headers  []string
	keys     []string
}

type testHeader struct {
	pos
	m       matcher
	headers []string
	keys    []string
}

type testExists struct {
	pos
	headers []string
}

type testSize struct {
	pos
	over bool
	n    int64
}

type testAllOf struct {
	pos
	tests []test
	any   bool // anyof
}

type testNot struct {
	pos
	t test
}

type testConst struct {
	pos
	v bool
}

type testBody struct {
	pos
	m         matcher
	transform string // raw, content, text
	types     []string
	keys      []string
}

type testDate struct {
	pos
	m        matcher
	current  bool
	zone     string
	hasZone  bool
	original bool
	header   string
	part     string
	keys     []string
}

type testString struct {
	pos
	m       matcher
	sources []string
	keys    []string
}

type testHasFlag struct {
	pos
	m    matcher
	vars []string
	keys []string
}

type testMailboxExists struct {
	pos
	names []string
}

type testDuplicate struct {
	pos
	handle      string
	header      string
	uniqueID    string
	hasUniqueID bool
	seconds     int64
	hasSeconds  bool
	last        bool
}

type testSpamTest struct {
	pos
	m       matcher
	percent bool
	value   string
}

// Argument checking.

type valKind int

const (
	vNone valKind = iota
	vString
	vStrings
	vNumber
)

// argSpec describes the arguments of one command or test.
type argSpec struct {
	tags map[string]valKind
	// match: comparator and match type tags are allowed.
	match bool
	// addressPart: :all, :localpart, :domain are allowed.
	addressPart bool
	positional  []valKind
}

type parsedArgs struct {
	tags     map[string]arg // value of the tag (zero arg for vNone)
	pos      []arg
	m        matcher
	part     string
	partSeen bool
}

var relOps = []string{"gt", "ge", "lt", "le", "eq", "ne"}

func (c *compiler) args(n *node, spec argSpec) (*parsedArgs, error) {
	pa := &parsedArgs{tags: map[string]arg{}, m: matcher{comparator: "i;ascii-casemap", kind: "is"}}
	kindSeen, cmpSeen := false, false
	i := 0
	for i < len(n.args) && n.args[i].kind == argTag {
		a := n.args[i]
		i++
		value := func(want valKind) (arg, error) {
			if i >= len(n.args) {
				return arg{}, errorAt(a.line, ":%s needs a value", a.tag)
			}
			v := n.args[i]
			i++
			switch want {
			case vNumber:
				if v.kind != argNumber {
					return arg{}, errorAt(a.line, ":%s needs a number", a.tag)
				}
			case vString:
				if v.kind != argStrings || v.list {
					return arg{}, errorAt(a.line, ":%s needs a string", a.tag)
				}
			case vStrings:
				if v.kind != argStrings {
					return arg{}, errorAt(a.line, ":%s needs a string list", a.tag)
				}
			}
			return v, nil
		}
		switch {
		case spec.match && (a.tag == "is" || a.tag == "contains" || a.tag == "matches" || a.tag == "regex" || a.tag == "value" || a.tag == "count"):
			if kindSeen {
				return nil, errorAt(a.line, "only one match type is allowed")
			}
			kindSeen = true
			pa.m.kind = a.tag
			switch a.tag {
			case "regex":
				if err := c.need(a.line, "regex", ":regex"); err != nil {
					return nil, err
				}
			case "value", "count":
				if err := c.need(a.line, "relational", ":"+a.tag); err != nil {
					return nil, err
				}
				v, err := value(vString)
				if err != nil {
					return nil, err
				}
				op := strings.ToLower(v.strs[0])
				if !slices.Contains(relOps, op) {
					return nil, errorAt(a.line, "%q is not a relational operator", v.strs[0])
				}
				pa.m.rel = op
			}
		case spec.match && a.tag == "comparator":
			if cmpSeen {
				return nil, errorAt(a.line, "only one comparator is allowed")
			}
			cmpSeen = true
			v, err := value(vString)
			if err != nil {
				return nil, err
			}
			name := strings.ToLower(v.strs[0])
			switch name {
			case "i;octet", "i;ascii-casemap":
			case "i;ascii-numeric":
				if !c.exts["comparator-i;ascii-numeric"] {
					return nil, errorAt(a.line, `the comparator i;ascii-numeric needs require "comparator-i;ascii-numeric"`)
				}
			default:
				return nil, errorAt(a.line, "comparator %q is not supported", v.strs[0])
			}
			pa.m.comparator = name
		case spec.addressPart && (a.tag == "all" || a.tag == "localpart" || a.tag == "domain"):
			if pa.partSeen {
				return nil, errorAt(a.line, "only one address part is allowed")
			}
			pa.partSeen, pa.part = true, a.tag
		default:
			want, ok := spec.tags[a.tag]
			if !ok {
				return nil, errorAt(a.line, "%s does not take :%s", n.name, a.tag)
			}
			if _, dup := pa.tags[a.tag]; dup {
				return nil, errorAt(a.line, ":%s is given twice", a.tag)
			}
			if want == vNone {
				pa.tags[a.tag] = arg{kind: argTag, tag: a.tag, line: a.line}
				continue
			}
			v, err := value(want)
			if err != nil {
				return nil, err
			}
			pa.tags[a.tag] = v
		}
	}
	if pa.m.comparator == "i;ascii-numeric" && (pa.m.kind == "contains" || pa.m.kind == "matches" || pa.m.kind == "regex") {
		return nil, errorAt(n.line, "the comparator i;ascii-numeric cannot be used with :%s", pa.m.kind)
	}
	rest := n.args[i:]
	if len(rest) != len(spec.positional) {
		if len(rest) > len(spec.positional) && rest[len(spec.positional)].kind == argTag {
			return nil, errorAt(rest[len(spec.positional)].line, "%s does not take :%s here", n.name, rest[len(spec.positional)].tag)
		}
		return nil, errorAt(n.line, "%s needs %d argument(s), found %d", n.name, len(spec.positional), len(rest))
	}
	for j, want := range spec.positional {
		a := rest[j]
		switch want {
		case vNumber:
			if a.kind != argNumber {
				return nil, errorAt(a.line, "%s: argument %d must be a number", n.name, j+1)
			}
		case vString:
			if a.kind != argStrings || a.list {
				return nil, errorAt(a.line, "%s: argument %d must be a string", n.name, j+1)
			}
		case vStrings:
			if a.kind != argStrings {
				return nil, errorAt(a.line, "%s: argument %d must be a string list", n.name, j+1)
			}
		}
	}
	pa.pos = rest
	return pa, nil
}

func (c *compiler) need(line int, ext, what string) error {
	if !c.exts[ext] {
		return errorAt(line, "%s needs require %q", what, ext)
	}
	return nil
}

func noTests(n *node) error {
	if len(n.tests) > 0 || n.testList {
		return errorAt(n.line, "%s does not take a test", n.name)
	}
	return nil
}

func noBlock(n *node) error {
	if n.hasBlock {
		return errorAt(n.line, "%s does not take a block", n.name)
	}
	return nil
}

// checkKeys checks what can be checked of keys before a run: regular
// expressions without variables must compile.
func (c *compiler) checkKeys(line int, m matcher, keys []string) error {
	if m.kind == "regex" {
		for _, k := range keys {
			if len(k) > MaxRegexBytes {
				return errorAt(line, "regular expression longer than %d bytes", MaxRegexBytes)
			}
			if c.exts["variables"] && strings.Contains(k, "${") {
				continue
			}
			if _, _, err := compileRegex(k); err != nil {
				return errorAt(line, "%s", err.(*Error).Msg)
			}
		}
	}
	return nil
}

// Supported extensions and how require reports the refused ones.
var refusedExts = map[string]string{
	"reject":  "reject is not offered: mail is never refused after it was accepted",
	"ereject": "ereject is not offered: mail is never refused after it was accepted",
}

func (c *compiler) require(n *node) error {
	if err := noTests(n); err != nil {
		return err
	}
	if err := noBlock(n); err != nil {
		return err
	}
	if len(n.args) != 1 || n.args[0].kind != argStrings {
		return errorAt(n.line, "require needs a string list")
	}
	for _, e := range n.args[0].strs {
		e = strings.ToLower(e)
		if msg, ok := refusedExts[e]; ok {
			return errorAt(n.line, "%s", msg)
		}
		if !slices.Contains(Extensions, e) {
			return errorAt(n.line, "extension %q is not supported", e)
		}
		c.exts[e] = true
	}
	if c.exts["vacation-seconds"] && !c.exts["vacation"] {
		c.exts["vacation"] = true // RFC 6131: implies vacation
	}
	if c.exts["spamtestplus"] {
		c.exts["spamtest"] = true
	}
	return nil
}

// block checks a list of commands. top: require is allowed at its start.
func (c *compiler) block(nodes []*node, top bool) ([]command, error) {
	var out []command
	requiresDone := !top
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if n.name == "require" {
			if requiresDone {
				return nil, errorAt(n.line, "require is only allowed at the start of the script")
			}
			if err := c.require(n); err != nil {
				return nil, err
			}
			continue
		}
		requiresDone = true
		if n.name == "if" {
			cmd := &cmdIf{pos: pos{n.line}}
			if err := c.branch(cmd, n); err != nil {
				return nil, err
			}
			for i+1 < len(nodes) && nodes[i+1].name == "elsif" {
				i++
				if err := c.branch(cmd, nodes[i]); err != nil {
					return nil, err
				}
			}
			if i+1 < len(nodes) && nodes[i+1].name == "else" {
				i++
				e := nodes[i]
				if len(e.args) > 0 || len(e.tests) > 0 || e.testList || !e.hasBlock {
					return nil, errorAt(e.line, "else takes only a block")
				}
				b, err := c.block(e.block, false)
				if err != nil {
					return nil, err
				}
				if b == nil {
					b = []command{}
				}
				cmd.orElse = b
			}
			out = append(out, cmd)
			continue
		}
		if n.name == "elsif" || n.name == "else" {
			return nil, errorAt(n.line, "%s without if", n.name)
		}
		cmd, err := c.command(n)
		if err != nil {
			return nil, err
		}
		out = append(out, cmd)
	}
	return out, nil
}

func (c *compiler) branch(cmd *cmdIf, n *node) error {
	if len(n.args) > 0 || len(n.tests) != 1 || n.testList {
		return errorAt(n.line, "%s needs exactly one test", n.name)
	}
	if !n.hasBlock {
		return errorAt(n.line, "%s needs a block", n.name)
	}
	t, err := c.test(n.tests[0])
	if err != nil {
		return err
	}
	b, err := c.block(n.block, false)
	if err != nil {
		return err
	}
	cmd.conds = append(cmd.conds, t)
	cmd.blocks = append(cmd.blocks, b)
	return nil
}

func flagList(a arg) []string {
	var out []string
	for _, s := range a.strs {
		out = append(out, strings.Fields(s)...)
	}
	return out
}

func (c *compiler) command(n *node) (command, error) {
	if err := noTests(n); err != nil {
		return nil, err
	}
	if err := noBlock(n); err != nil {
		return nil, err
	}
	p := pos{n.line}
	switch n.name {
	case "stop":
		if _, err := c.args(n, argSpec{}); err != nil {
			return nil, err
		}
		return &cmdStop{p}, nil
	case "discard":
		if _, err := c.args(n, argSpec{}); err != nil {
			return nil, err
		}
		return &cmdDiscard{p}, nil
	case "keep":
		spec := argSpec{}
		if c.exts["imap4flags"] {
			spec.tags = map[string]valKind{"flags": vStrings}
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		k := &cmdKeep{pos: p}
		if f, ok := pa.tags["flags"]; ok {
			k.flags, k.hasFlags = f.strs, true
		}
		return k, nil
	case "fileinto":
		if err := c.need(n.line, "fileinto", "fileinto"); err != nil {
			return nil, err
		}
		spec := argSpec{tags: map[string]valKind{}, positional: []valKind{vString}}
		if c.exts["copy"] {
			spec.tags["copy"] = vNone
		}
		if c.exts["mailbox"] {
			spec.tags["create"] = vNone
		}
		if c.exts["imap4flags"] {
			spec.tags["flags"] = vStrings
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		f := &cmdFileinto{pos: p, folder: pa.pos[0].strs[0]}
		_, f.copy = pa.tags["copy"]
		_, f.create = pa.tags["create"]
		if fl, ok := pa.tags["flags"]; ok {
			f.flags, f.hasFlags = fl.strs, true
		}
		if f.folder == "" {
			return nil, errorAt(n.line, "fileinto needs a folder name")
		}
		return f, nil
	case "redirect":
		spec := argSpec{tags: map[string]valKind{}, positional: []valKind{vString}}
		if c.exts["copy"] {
			spec.tags["copy"] = vNone
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		r := &cmdRedirect{pos: p, addr: pa.pos[0].strs[0]}
		_, r.copy = pa.tags["copy"]
		if !(c.exts["variables"] && strings.Contains(r.addr, "${")) && !validRedirect(r.addr) {
			return nil, errorAt(n.line, "redirect needs one e-mail address, found %q", r.addr)
		}
		return r, nil
	case "set":
		if err := c.need(n.line, "variables", "set"); err != nil {
			return nil, err
		}
		return c.set(n)
	case "setflag", "addflag", "removeflag":
		if err := c.need(n.line, "imap4flags", n.name); err != nil {
			return nil, err
		}
		f := &cmdFlags{pos: p}
		switch n.name {
		case "addflag":
			f.op = flagAdd
		case "removeflag":
			f.op = flagRemove
		}
		var spec argSpec
		switch len(n.args) {
		case 1:
			spec.positional = []valKind{vStrings}
		case 2:
			spec.positional = []valKind{vString, vStrings}
		default:
			return nil, errorAt(n.line, "%s needs a flag list and optionally a variable name before it", n.name)
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		if len(pa.pos) == 2 {
			if err := c.need(n.line, "variables", "a variable name in "+n.name); err != nil {
				return nil, err
			}
			f.varname = strings.ToLower(pa.pos[0].strs[0])
			if !validVarName(f.varname) {
				return nil, errorAt(n.line, "%q is not a valid variable name", pa.pos[0].strs[0])
			}
		}
		f.flags = pa.pos[len(pa.pos)-1].strs
		return f, nil
	case "vacation":
		if err := c.need(n.line, "vacation", "vacation"); err != nil {
			return nil, err
		}
		return c.vacation(n)
	case "include":
		if err := c.need(n.line, "include", "include"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{tags: map[string]valKind{"personal": vNone, "global": vNone, "once": vNone, "optional": vNone}, positional: []valKind{vString}})
		if err != nil {
			return nil, err
		}
		if _, ok := pa.tags["global"]; ok {
			return nil, errorAt(n.line, "include :global is not offered: only your own scripts can be included")
		}
		inc := &cmdInclude{pos: p, name: pa.pos[0].strs[0]}
		_, inc.once = pa.tags["once"]
		_, inc.optional = pa.tags["optional"]
		if !ValidScriptName(inc.name) {
			return nil, errorAt(n.line, "%q is not a valid script name", inc.name)
		}
		return inc, nil
	case "return":
		if err := c.need(n.line, "include", "return"); err != nil {
			return nil, err
		}
		if _, err := c.args(n, argSpec{}); err != nil {
			return nil, err
		}
		return &cmdReturn{p}, nil
	case "global":
		if err := c.need(n.line, "include", "global"); err != nil {
			return nil, err
		}
		if err := c.need(n.line, "variables", "global"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{positional: []valKind{vStrings}})
		if err != nil {
			return nil, err
		}
		g := &cmdGlobal{pos: p}
		for _, name := range pa.pos[0].strs {
			name = strings.ToLower(name)
			if !validVarName(name) {
				return nil, errorAt(n.line, "%q is not a valid variable name", name)
			}
			g.names = append(g.names, name)
		}
		return g, nil
	case "reject", "ereject":
		return nil, errorAt(n.line, "%s", refusedExts[n.name])
	}
	return nil, errorAt(n.line, "unknown command %q", n.name)
}

// set modifiers by precedence (RFC 5229 4.1, 15 for quoteregex from the
// regex draft); a higher precedence applies first.
var setModifiers = map[string]int{
	"lower": 40, "upper": 40, "lowerfirst": 30, "upperfirst": 30,
	"quotewildcard": 20, "quoteregex": 15, "length": 10,
}

func (c *compiler) set(n *node) (command, error) {
	s := &cmdSet{pos: pos{n.line}}
	prec := map[int]bool{}
	i := 0
	for ; i < len(n.args) && n.args[i].kind == argTag; i++ {
		t := n.args[i].tag
		pr, ok := setModifiers[t]
		if !ok {
			return nil, errorAt(n.line, "set does not take :%s", t)
		}
		if t == "quoteregex" {
			if err := c.need(n.line, "regex", ":quoteregex"); err != nil {
				return nil, err
			}
		}
		if prec[pr] {
			return nil, errorAt(n.line, "set takes only one modifier of the kind of :%s", t)
		}
		prec[pr] = true
		s.mods = append(s.mods, t)
	}
	rest := n.args[i:]
	if len(rest) != 2 || rest[0].kind != argStrings || rest[0].list || rest[1].kind != argStrings || rest[1].list {
		return nil, errorAt(n.line, "set needs a variable name and a value")
	}
	s.name = strings.ToLower(rest[0].strs[0])
	if !validVarName(s.name) {
		return nil, errorAt(n.line, "%q is not a valid variable name", rest[0].strs[0])
	}
	s.value = rest[1].strs[0]
	slices.SortStableFunc(s.mods, func(a, b string) int { return setModifiers[b] - setModifiers[a] })
	return s, nil
}

func (c *compiler) vacation(n *node) (command, error) {
	spec := argSpec{
		tags:       map[string]valKind{"days": vNumber, "subject": vString, "from": vString, "addresses": vStrings, "mime": vNone, "handle": vString},
		positional: []valKind{vString},
	}
	if c.exts["vacation-seconds"] {
		spec.tags["seconds"] = vNumber
	}
	pa, err := c.args(n, spec)
	if err != nil {
		return nil, err
	}
	v := &cmdVacation{pos: pos{n.line}, reason: pa.pos[0].strs[0]}
	d, hasDays := pa.tags["days"]
	sec, hasSeconds := pa.tags["seconds"]
	switch {
	case hasDays && hasSeconds:
		return nil, errorAt(n.line, "vacation takes either :days or :seconds")
	case hasDays:
		v.period, v.hasPeriod = d.num*86400, true
	case hasSeconds:
		v.period, v.hasPeriod = sec.num, true
	}
	if a, ok := pa.tags["subject"]; ok {
		v.subject, v.hasSubject = a.strs[0], true
	}
	if a, ok := pa.tags["from"]; ok {
		v.from, v.hasFrom = a.strs[0], true
	}
	if a, ok := pa.tags["addresses"]; ok {
		v.addresses = a.strs
	}
	if a, ok := pa.tags["handle"]; ok {
		v.handle, v.hasHandle = a.strs[0], true
	}
	_, v.mime = pa.tags["mime"]
	return v, nil
}

func (c *compiler) tests(nodes []*node) ([]test, error) {
	out := make([]test, 0, len(nodes))
	for _, n := range nodes {
		t, err := c.test(n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (c *compiler) test(n *node) (test, error) {
	if n.hasBlock {
		return nil, errorAt(n.line, "a test does not take a block")
	}
	p := pos{n.line}
	switch n.name {
	case "allof", "anyof":
		if len(n.args) > 0 || !n.testList || len(n.tests) == 0 {
			return nil, errorAt(n.line, "%s needs a list of tests in ( )", n.name)
		}
		ts, err := c.tests(n.tests)
		if err != nil {
			return nil, err
		}
		return &testAllOf{pos: p, tests: ts, any: n.name == "anyof"}, nil
	case "not":
		if len(n.args) > 0 || n.testList || len(n.tests) != 1 {
			return nil, errorAt(n.line, "not needs exactly one test")
		}
		t, err := c.test(n.tests[0])
		if err != nil {
			return nil, err
		}
		return &testNot{pos: p, t: t}, nil
	}
	if err := noTests(n); err != nil {
		return nil, err
	}
	switch n.name {
	case "true", "false":
		if _, err := c.args(n, argSpec{}); err != nil {
			return nil, err
		}
		return &testConst{pos: p, v: n.name == "true"}, nil
	case "address", "envelope":
		if n.name == "envelope" {
			if err := c.need(n.line, "envelope", "envelope"); err != nil {
				return nil, err
			}
		}
		pa, err := c.args(n, argSpec{match: true, addressPart: true, positional: []valKind{vStrings, vStrings}})
		if err != nil {
			return nil, err
		}
		t := &testAddress{pos: p, m: pa.m, part: "all", envelope: n.name == "envelope", headers: lower(pa.pos[0].strs), keys: pa.pos[1].strs}
		if pa.partSeen {
			t.part = pa.part
		}
		if t.envelope {
			for _, h := range t.headers {
				if h != "from" && h != "to" && !(c.exts["variables"] && strings.Contains(h, "${")) {
					return nil, errorAt(n.line, "envelope knows only \"from\" and \"to\", not %q", h)
				}
			}
		}
		return t, c.checkKeys(n.line, t.m, t.keys)
	case "header":
		pa, err := c.args(n, argSpec{match: true, positional: []valKind{vStrings, vStrings}})
		if err != nil {
			return nil, err
		}
		t := &testHeader{pos: p, m: pa.m, headers: lower(pa.pos[0].strs), keys: pa.pos[1].strs}
		return t, c.checkKeys(n.line, t.m, t.keys)
	case "exists":
		pa, err := c.args(n, argSpec{positional: []valKind{vStrings}})
		if err != nil {
			return nil, err
		}
		return &testExists{pos: p, headers: lower(pa.pos[0].strs)}, nil
	case "size":
		pa, err := c.args(n, argSpec{tags: map[string]valKind{"over": vNone, "under": vNone}, positional: []valKind{vNumber}})
		if err != nil {
			return nil, err
		}
		_, over := pa.tags["over"]
		_, under := pa.tags["under"]
		if over == under {
			return nil, errorAt(n.line, "size needs either :over or :under")
		}
		return &testSize{pos: p, over: over, n: pa.pos[0].num}, nil
	case "body":
		if err := c.need(n.line, "body", "body"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{match: true, tags: map[string]valKind{"raw": vNone, "content": vStrings, "text": vNone}, positional: []valKind{vStrings}})
		if err != nil {
			return nil, err
		}
		t := &testBody{pos: p, m: pa.m, transform: "text", keys: pa.pos[0].strs}
		n := 0
		if _, ok := pa.tags["raw"]; ok {
			t.transform = "raw"
			n++
		}
		if ct, ok := pa.tags["content"]; ok {
			t.transform, t.types = "content", lower(ct.strs)
			n++
		}
		if _, ok := pa.tags["text"]; ok {
			n++
		}
		if n > 1 {
			return nil, errorAt(p.ln, "body takes only one of :raw, :content and :text")
		}
		return t, c.checkKeys(p.ln, t.m, t.keys)
	case "date", "currentdate":
		if err := c.need(n.line, "date", n.name); err != nil {
			return nil, err
		}
		spec := argSpec{match: true, tags: map[string]valKind{"zone": vString}}
		if n.name == "date" {
			spec.tags["originalzone"] = vNone
			spec.positional = []valKind{vString, vString, vStrings}
		} else {
			spec.positional = []valKind{vString, vStrings}
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		t := &testDate{pos: p, m: pa.m, current: n.name == "currentdate"}
		if z, ok := pa.tags["zone"]; ok {
			t.zone, t.hasZone = z.strs[0], true
			if _, ok := parseZone(t.zone); !ok && !(c.exts["variables"] && strings.Contains(t.zone, "${")) {
				return nil, errorAt(n.line, "%q is not a time zone offset like +0100", t.zone)
			}
		}
		_, t.original = pa.tags["originalzone"]
		if t.original && t.hasZone {
			return nil, errorAt(n.line, "date takes either :zone or :originalzone")
		}
		i := 0
		if !t.current {
			t.header = strings.ToLower(pa.pos[0].strs[0])
			i = 1
		}
		t.part = strings.ToLower(pa.pos[i].strs[0])
		if !slices.Contains(dateParts, t.part) && !(c.exts["variables"] && strings.Contains(t.part, "${")) {
			return nil, errorAt(n.line, "%q is not a date part", t.part)
		}
		t.keys = pa.pos[i+1].strs
		return t, c.checkKeys(n.line, t.m, t.keys)
	case "string":
		if err := c.need(n.line, "variables", "string"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{match: true, positional: []valKind{vStrings, vStrings}})
		if err != nil {
			return nil, err
		}
		t := &testString{pos: p, m: pa.m, sources: pa.pos[0].strs, keys: pa.pos[1].strs}
		return t, c.checkKeys(n.line, t.m, t.keys)
	case "hasflag":
		if err := c.need(n.line, "imap4flags", "hasflag"); err != nil {
			return nil, err
		}
		// hasflag [variable-list] <list-of-flags>: try two lists first.
		pa, err := c.args(n, argSpec{match: true, positional: []valKind{vStrings, vStrings}})
		if err != nil {
			var err1 error
			if pa, err1 = c.args(n, argSpec{match: true, positional: []valKind{vStrings}}); err1 != nil {
				return nil, err
			}
		}
		t := &testHasFlag{pos: p, m: pa.m}
		if len(pa.pos) == 2 {
			if err := c.need(n.line, "variables", "variable names in hasflag"); err != nil {
				return nil, err
			}
			t.vars = lower(pa.pos[0].strs)
		}
		t.keys = pa.pos[len(pa.pos)-1].strs
		return t, c.checkKeys(n.line, t.m, t.keys)
	case "mailboxexists":
		if err := c.need(n.line, "mailbox", "mailboxexists"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{positional: []valKind{vStrings}})
		if err != nil {
			return nil, err
		}
		return &testMailboxExists{pos: p, names: pa.pos[0].strs}, nil
	case "duplicate":
		if err := c.need(n.line, "duplicate", "duplicate"); err != nil {
			return nil, err
		}
		pa, err := c.args(n, argSpec{tags: map[string]valKind{"handle": vString, "header": vString, "uniqueid": vString, "seconds": vNumber, "last": vNone}})
		if err != nil {
			return nil, err
		}
		t := &testDuplicate{pos: p, header: "message-id"}
		if a, ok := pa.tags["handle"]; ok {
			t.handle = a.strs[0]
		}
		h, hasHeader := pa.tags["header"]
		u, hasUID := pa.tags["uniqueid"]
		if hasHeader && hasUID {
			return nil, errorAt(n.line, "duplicate takes either :header or :uniqueid")
		}
		if hasHeader {
			t.header = strings.ToLower(h.strs[0])
		}
		if hasUID {
			t.uniqueID, t.hasUniqueID = u.strs[0], true
		}
		if a, ok := pa.tags["seconds"]; ok {
			t.seconds, t.hasSeconds = a.num, true
		}
		_, t.last = pa.tags["last"]
		return t, nil
	case "spamtest":
		if err := c.need(n.line, "spamtest", "spamtest"); err != nil {
			return nil, err
		}
		spec := argSpec{match: true, tags: map[string]valKind{}, positional: []valKind{vString}}
		if c.exts["spamtestplus"] {
			spec.tags["percent"] = vNone
		}
		pa, err := c.args(n, spec)
		if err != nil {
			return nil, err
		}
		t := &testSpamTest{pos: p, m: pa.m, value: pa.pos[0].strs[0]}
		_, t.percent = pa.tags["percent"]
		return t, nil
	}
	return nil, errorAt(n.line, "unknown test %q", n.name)
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func validVarName(s string) bool {
	if s == "" || len(s) > 64 || !isIdentStart(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
}

// validRedirect tells whether s looks like one address local@domain.
func validRedirect(s string) bool {
	s = strings.TrimSpace(s)
	i := strings.LastIndexByte(s, '@')
	if i <= 0 || i == len(s)-1 || len(s) > 254 {
		return false
	}
	for j := 0; j < len(s); j++ {
		if c := s[j]; c <= ' ' || c >= 0x7f || c == '<' || c == '>' || c == ',' || c == '"' {
			return false
		}
	}
	return true
}

// ValidScriptName tells whether a script name is acceptable: 1 to 128
// characters, no control characters and no "/" (RFC 5804 1.6, stricter).
func ValidScriptName(name string) bool {
	if name == "" || len([]rune(name)) > 128 {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 || r == '/' {
			return false
		}
	}
	return true
}
