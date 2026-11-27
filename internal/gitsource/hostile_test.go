package gitsource

// Regression tests for the security review of #137: hostile packs and
// servers that are small to send but expensive to handle. Each must fail
// (or pass) quickly and within its budget.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rawObj struct {
	typ  int
	data []byte
}

func objID(typ int, data []byte) string {
	h := sha1.New()
	h.Write([]byte(typeName(typ) + " " + strconv.Itoa(len(data)) + "\x00"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// buildPack packs whole objects (no deltas) the way a server would.
func buildPack(objs []rawObj) []byte {
	var b bytes.Buffer
	b.WriteString("PACK")
	binary.Write(&b, binary.BigEndian, uint32(2))
	binary.Write(&b, binary.BigEndian, uint32(len(objs)))
	for _, o := range objs {
		size := len(o.data)
		c := byte(o.typ<<4) | byte(size&0x0f)
		size >>= 4
		for size > 0 {
			b.WriteByte(c | 0x80)
			c = byte(size & 0x7f)
			size >>= 7
		}
		b.WriteByte(c)
		zw, _ := zlib.NewWriterLevel(&b, zlib.BestCompression)
		zw.Write(o.data)
		zw.Close()
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}

func treeEntry(mode, name, id string) []byte {
	raw, _ := hex.DecodeString(id)
	return append([]byte(mode+" "+name+"\x00"), raw...)
}

// packWithTree packs a commit of the tree root and the other objects, and
// returns the store and the root tree.
func packWithTree(t *testing.T, root []byte, others ...rawObj) (*packStore, *object) {
	t.Helper()
	commit := []byte("tree " + objID(objTree, root) + "\n\nx\n")
	objs := append([]rawObj{{objCommit, commit}, {objTree, root}}, others...)
	s, err := readPack(bytes.NewReader(buildPack(objs)), t.TempDir(), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	r, err := commitTree(s, objID(objCommit, commit))
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func wantLimit(t *testing.T, err error, msg string) {
	t.Helper()
	var le *LimitError
	if !errors.As(err, &le) || !strings.Contains(le.Msg, msg) {
		t.Fatalf("got %v, want a limit about %q", err, msg)
	}
}

// H2: one large tree of submodules, named from many folders. Submodules
// count as entries, and every listing read counts, however often a tree is
// named.
func TestHostileGitlinkTree(t *testing.T) {
	var heavy bytes.Buffer
	zero := strings.Repeat("00", 20)
	for i := range 5000 {
		heavy.Write(treeEntry("160000", fmt.Sprintf("m%05d", i), zero))
	}
	heavyID := objID(objTree, heavy.Bytes())
	var parent bytes.Buffer
	for i := range 200 {
		parent.Write(treeEntry("40000", fmt.Sprintf("d%03d", i), heavyID))
	}
	s, root := packWithTree(t, parent.Bytes(), rawObj{objTree, heavy.Bytes()})
	start := time.Now()
	_, err := walk(context.Background(), s, root, DefaultLimits.withDefaults())
	wantLimit(t, err, "more than 100000 files and folders")
	lim := DefaultLimits
	lim.TreeBytes = 1 << 20
	_, err = walk(context.Background(), s, root, lim)
	wantLimit(t, err, "folder listings are larger than")
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("took %v", el)
	}
	// A cancelled context stops the walk.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := walk(ctx, s, root, DefaultLimits); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// H1: links leading through a chain of 40 links with long targets, from
// many starting links. The work is bounded by the budget, not by the
// number of links times hops times parts.
func TestHostileLinkChain(t *testing.T) {
	var objs []rawObj
	var tr bytes.Buffer
	type ent struct{ name, id string }
	var ents []ent
	for i := 1; i <= 40; i++ {
		tgt := strings.Repeat("x/", 120) + strings.Repeat("../", 120) + fmt.Sprintf("c%02d", i+1)
		objs = append(objs, rawObj{objBlob, []byte(tgt)})
		ents = append(ents, ent{fmt.Sprintf("c%02d", i), objID(objBlob, []byte(tgt))})
	}
	// Every start has a target of its own, so no result can be reused.
	for i := range 5000 {
		start := []byte(fmt.Sprintf("n%05d/../c01", i))
		objs = append(objs, rawObj{objBlob, start})
		ents = append(ents, ent{fmt.Sprintf("s%05d", i), objID(objBlob, start)})
	}
	for _, e := range ents {
		tr.Write(treeEntry("120000", e.name, e.id))
	}
	s, root := packWithTree(t, tr.Bytes(), objs...)
	entries, err := walk(context.Background(), s, root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	_, err = checkLinks(context.Background(), s, entries, DefaultLimits)
	wantLimit(t, err, "lead through each other too often")
	if el := time.Since(t0); el > 5*time.Second {
		t.Fatalf("took %v", el)
	}
	// Parts per target are limited.
	long := strings.Repeat("a/", 300) + "b"
	var one bytes.Buffer
	one.Write(treeEntry("120000", "l", objID(objBlob, []byte(long))))
	s, root = packWithTree(t, one.Bytes(), rawObj{objBlob, []byte(long)})
	entries, _ = walk(context.Background(), s, root, DefaultLimits)
	_, err = checkLinks(context.Background(), s, entries, DefaultLimits)
	wantLimit(t, err, "more than 256 parts")
}

// Links that are fine are still accepted, through other links too.
func TestLinksThroughLinks(t *testing.T) {
	var tr, sub bytes.Buffer
	blob := []byte("x")
	sub.Write(treeEntry("100644", "f", objID(objBlob, blob)))
	sub.Write(treeEntry("120000", "up", objID(objBlob, []byte(".."))))
	tr.Write(treeEntry("40000", "d", objID(objTree, sub.Bytes())))
	tr.Write(treeEntry("120000", "a", objID(objBlob, []byte("d/up/d/f"))))
	tr.Write(treeEntry("120000", "b", objID(objBlob, []byte("d/up/../x"))))
	s, root := packWithTree(t, tr.Bytes(), rawObj{objTree, sub.Bytes()}, rawObj{objBlob, blob}, rawObj{objBlob, []byte("..")},
		rawObj{objBlob, []byte("d/up/d/f")}, rawObj{objBlob, []byte("d/up/../x")})
	entries, err := walk(context.Background(), s, root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = checkLinks(context.Background(), s, entries, DefaultLimits)
	var te *TreeError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, `"b"`) {
		t.Fatalf("got %v, want link b refused", err)
	}
}

// Many links naming one long target read it once, and distinct targets
// together are limited.
func TestHostileLinkMemory(t *testing.T) {
	target := strings.Repeat("a", 4000)
	id := objID(objBlob, []byte(target))
	var tr bytes.Buffer
	for i := range 20000 {
		tr.Write(treeEntry("120000", fmt.Sprintf("l%05d", i), id))
	}
	s, root := packWithTree(t, tr.Bytes(), rawObj{objBlob, []byte(target)})
	entries, err := walk(context.Background(), s, root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	links, err := checkLinks(context.Background(), s, entries, DefaultLimits)
	runtime.ReadMemStats(&m2)
	if err != nil || len(links) != 20000 {
		t.Fatalf("%d links, %v", len(links), err)
	}
	if got := (m2.TotalAlloc - m1.TotalAlloc) >> 20; got > 20 {
		t.Fatalf("checking 20000 links to one target allocated %d MB", got)
	}
}

// L1: a delta that announces a large result allocates only what it writes.
func TestHostileDeltaSize(t *testing.T) {
	base := []byte("x")
	delta := []byte{1, 0x80, 0x80, 0x80, 0x30, 0x90} // announces 100 MB
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	for range 10 {
		if _, err := applyDelta(base, delta, 100<<20); err == nil {
			t.Fatal("accepted")
		}
	}
	runtime.ReadMemStats(&m2)
	if got := (m2.TotalAlloc - m1.TotalAlloc) >> 20; got > 5 {
		t.Fatalf("10 failing deltas allocated %d MB", got)
	}
}

// M1: an advertisement keeps only the branches asked for, and is limited.
func TestHostileAdvert(t *testing.T) {
	var b bytes.Buffer
	oid := strings.Repeat("1", 40)
	writePkt(&b, oid+" HEAD\x00side-band-64k symref=HEAD:refs/heads/main\n")
	writePkt(&b, oid+" refs/heads/main\n")
	for i := range 3000 {
		writePkt(&b, fmt.Sprintf("%s refs/heads/%s%05d\n", oid, strings.Repeat("x", 200), i))
		writePkt(&b, fmt.Sprintf("%s refs/tags/v%05d\n", oid, i))
	}
	writeFlush(&b)
	ad, err := readAdvert(newPktReader(bytes.NewReader(b.Bytes())), "main", maxAdvertBytes)
	if err != nil || len(ad.refs) != 1 || ad.refs["refs/heads/main"] != oid {
		t.Fatalf("one branch: %d refs, %v", len(ad.refs), err)
	}
	ad, err = readAdvert(newPktReader(bytes.NewReader(b.Bytes())), "", maxAdvertBytes)
	if err != nil || len(ad.refs) != MaxBranches || !ad.truncated {
		t.Fatalf("all branches: %d refs, truncated %v, %v", len(ad.refs), ad.truncated, err)
	}
	_, err = readAdvert(newPktReader(bytes.NewReader(b.Bytes())), "", 100_000)
	wantLimit(t, err, "too many branches")
}

// L2: side-band progress and empty packets count against the transfer
// limit, also when no pack data arrives.
func TestHostileSidebandNoise(t *testing.T) {
	var b bytes.Buffer
	writePkt(&b, "NAK\n")
	for range 2000 {
		writePkt(&b, "\x02"+strings.Repeat("p", 1000))
	}
	raw := &rawLimit{r: bytes.NewReader(b.Bytes()), max: 100_000}
	p := newPktReader(raw)
	if err := readUntilPack(p); err != nil {
		t.Fatal(err)
	}
	_, err := (&sideband{p: p}).Read(make([]byte, 10))
	wantLimit(t, err, "to download")
}

// L6: names with control characters are refused.
func TestNamesWithControlCharacters(t *testing.T) {
	for _, n := range []string{"a\tb", "a\x1bb", "a\x7fb", "\x01"} {
		var te *TreeError
		if err := checkName(n); !errors.As(err, &te) {
			t.Errorf("%q: %v", n, err)
		}
	}
	if err := checkName("ä b-c_d.txt"); err != nil {
		t.Error(err)
	}
}

// L4: the server's own public addresses are refused like private ones.
func TestOwnAddressesRefused(t *testing.T) {
	allowPrivate = false
	defer func() { allowPrivate = true }()
	own := netip.MustParseAddr("203.0.114.7")
	SetOwnAddresses(func() []netip.Addr { return []netip.Addr{own} })
	defer ownAddresses.Store(nil)
	if publicAddr(own) || publicAddr(netip.MustParseAddr("::ffff:203.0.114.7")) {
		t.Fatal("the server's own address was allowed")
	}
	if !publicAddr(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("a public address was refused")
	}
	if _, err := ParseAddress("git@203.0.114.7:a/b.git"); err == nil {
		t.Fatal("an address naming the server itself was accepted")
	}
}
