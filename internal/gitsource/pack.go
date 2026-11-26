package gitsource

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strconv"
)

// Reading a pack: every object is inflated into a temporary file (so the
// panel's memory holds only an index and one object at a time), deltas are
// resolved against their bases, and objects are found by their id. The
// pack is untrusted: every count and size is checked against Limits before
// it is used, and the object ids are computed here, never taken from the
// server.

// Object types.
const (
	objCommit   = 1
	objTree     = 2
	objBlob     = 3
	objTag      = 4
	objOfsDelta = 6
	objRefDelta = 7
)

// LimitError says which limit a fetch ran into. Its text is meant for
// people.
type LimitError struct{ Msg string }

func (e *LimitError) Error() string { return e.Msg }

func limitf(format string, args ...any) error {
	return &LimitError{Msg: fmt.Sprintf(format, args...)}
}

type object struct {
	typ  int   // after resolving: commit, tree, blob or tag
	off  int64 // where its (inflated) data starts in the store file
	size int64
	id   string // hex, after resolving
	// For deltas until resolved: the base, by offset in the pack or by id.
	baseOff int64
	baseID  string
	delta   bool
}

type packStore struct {
	f       *os.File
	written int64
	lim     Limits
	objs    []*object
	byID    map[string]*object
}

func (s *packStore) close() {
	if s.f != nil {
		s.f.Close()
		os.Remove(s.f.Name())
	}
}

// countingReader counts the bytes read and refuses more than max.
type countingReader struct {
	r   *bufio.Reader
	n   int64
	max int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	if c.n >= c.max {
		return 0, limitf("the repository is larger than %s to download", byteSize(c.max))
	}
	if int64(len(b)) > c.max-c.n {
		b = b[:c.max-c.n]
	}
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) ReadByte() (byte, error) {
	if c.n >= c.max {
		return 0, limitf("the repository is larger than %s to download", byteSize(c.max))
	}
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

// readPack reads a whole pack from r into a new store in dir.
func readPack(r io.Reader, dir string, lim Limits) (*packStore, error) {
	f, err := os.CreateTemp(dir, "objects-*")
	if err != nil {
		return nil, err
	}
	s := &packStore{f: f, lim: lim, byID: map[string]*object{}}
	if err := s.read(r); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *packStore) read(raw io.Reader) error {
	cr := &countingReader{r: bufio.NewReaderSize(raw, 64<<10), max: s.lim.TransferBytes}
	sum := sha1.New()
	// The pack's own checksum covers everything before it.
	tee := &hashingReader{r: cr, h: sum}
	var head [12]byte
	if _, err := io.ReadFull(tee, head[:]); err != nil {
		return packErr(err)
	}
	if string(head[:4]) != "PACK" {
		return protocolf("no pack")
	}
	if v := binary.BigEndian.Uint32(head[4:8]); v != 2 && v != 3 {
		return protocolf("unknown pack version")
	}
	count := int64(binary.BigEndian.Uint32(head[8:12]))
	if count > int64(s.lim.Objects) {
		return limitf("the commit has more than %d objects", s.lim.Objects)
	}
	s.objs = make([]*object, 0, count)
	byOff := make(map[int64]*object, count)
	w := bufio.NewWriterSize(s.f, 256<<10)
	for range count {
		start := tee.n
		typ, size, err := readObjectHeader(tee)
		if err != nil {
			return packErr(err)
		}
		o := &object{typ: typ, size: size}
		switch typ {
		case objCommit, objTree, objBlob, objTag:
		case objOfsDelta:
			rel, err := readOfsOffset(tee)
			if err != nil {
				return packErr(err)
			}
			if rel <= 0 || rel > start {
				return protocolf("bad delta base")
			}
			o.delta, o.baseOff = true, start-rel
		case objRefDelta:
			var id [20]byte
			if _, err := io.ReadFull(tee, id[:]); err != nil {
				return packErr(err)
			}
			o.delta, o.baseID = true, hex.EncodeToString(id[:])
		default:
			return protocolf("unknown object type %d", typ)
		}
		if size > s.lim.ObjectBytes {
			return limitf("the repository contains a file or object larger than %s", byteSize(s.lim.ObjectBytes))
		}
		o.off = s.written
		if err := s.inflate(tee, w, size); err != nil {
			return err
		}
		s.objs = append(s.objs, o)
		byOff[start] = o
	}
	// The trailer: the checksum of the pack so far.
	want := sum.Sum(nil)
	var got [20]byte
	if _, err := io.ReadFull(cr, got[:]); err != nil {
		return packErr(err)
	}
	if !bytes.Equal(want, got[:]) {
		return protocolf("the pack's checksum does not match")
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// Ids of whole objects.
	for _, o := range s.objs {
		if !o.delta {
			id, err := s.hashObject(o)
			if err != nil {
				return err
			}
			o.id = id
			if _, dup := s.byID[id]; !dup {
				s.byID[id] = o
			}
		}
	}
	return s.resolve(byOff)
}

type hashingReader struct {
	r *countingReader
	h hash.Hash
	n int64
}

func (h *hashingReader) Read(b []byte) (int, error) {
	n, err := h.r.Read(b)
	h.h.Write(b[:n])
	h.n += int64(n)
	return n, err
}

func (h *hashingReader) ReadByte() (byte, error) {
	b, err := h.r.ReadByte()
	if err == nil {
		h.h.Write([]byte{b})
		h.n++
	}
	return b, err
}

func packErr(err error) error {
	var le *LimitError
	var se *ServerError
	var pe *ProtocolError
	if errors.As(err, &le) || errors.As(err, &se) || errors.As(err, &pe) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return protocolf("the pack ended early")
	}
	return err
}

func readObjectHeader(r io.ByteReader) (int, int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	typ := int(b>>4) & 7
	size := int64(b & 0x0f)
	shift := uint(4)
	for b&0x80 != 0 {
		if b, err = r.ReadByte(); err != nil {
			return 0, 0, err
		}
		if shift > 56 {
			return 0, 0, protocolf("object size too large")
		}
		size |= int64(b&0x7f) << shift
		shift += 7
	}
	return typ, size, nil
}

func readOfsOffset(r io.ByteReader) (int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	off := int64(b & 0x7f)
	for b&0x80 != 0 {
		if b, err = r.ReadByte(); err != nil {
			return 0, err
		}
		if off > 1<<55 {
			return 0, protocolf("bad delta offset")
		}
		off = ((off + 1) << 7) | int64(b&0x7f)
	}
	return off, nil
}

// inflate reads one zlib stream of exactly size bytes into w.
func (s *packStore) inflate(r io.Reader, w *bufio.Writer, size int64) error {
	if s.written+size > s.lim.StoreBytes {
		return limitf("the repository is larger than %s unpacked", byteSize(s.lim.StoreBytes))
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return packErr(protocolf("bad compressed object"))
	}
	n, err := io.Copy(w, io.LimitReader(zr, size+1))
	if err != nil {
		return packErr(nilIfZlib(err))
	}
	if n != size {
		return protocolf("an object's size does not match")
	}
	// Read the end of the stream (and its checksum).
	var one [1]byte
	if m, err := zr.Read(one[:]); m != 0 || err != io.EOF {
		if err == nil || m != 0 {
			return protocolf("an object's size does not match")
		}
		return packErr(nilIfZlib(err))
	}
	s.written += size
	return nil
}

func nilIfZlib(err error) error {
	var le *LimitError
	if errors.As(err, &le) {
		return err
	}
	if errors.Is(err, zlib.ErrChecksum) || errors.Is(err, zlib.ErrHeader) {
		return protocolf("bad compressed object")
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return protocolf("the pack ended early")
	}
	return protocolf("bad compressed object")
}

func typeName(t int) string {
	switch t {
	case objCommit:
		return "commit"
	case objTree:
		return "tree"
	case objBlob:
		return "blob"
	case objTag:
		return "tag"
	}
	return ""
}

func (s *packStore) section(o *object) *io.SectionReader {
	return io.NewSectionReader(s.f, o.off, o.size)
}

func (s *packStore) hashObject(o *object) (string, error) {
	h := sha1.New()
	h.Write([]byte(typeName(o.typ) + " " + strconv.FormatInt(o.size, 10) + "\x00"))
	if _, err := io.Copy(h, s.section(o)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// data reads a whole object into memory (at most Limits.ObjectBytes).
func (s *packStore) data(o *object) ([]byte, error) {
	b := make([]byte, o.size)
	if _, err := io.ReadFull(s.section(o), b); err != nil {
		return nil, err
	}
	return b, nil
}

// resolve applies every delta to its base, once, in an order where the
// base is always resolved first.
func (s *packStore) resolve(byOff map[int64]*object) error {
	waitOff := map[int64][]*object{}
	waitID := map[string][]*object{}
	var ready []*object
	pending := 0
	for _, o := range s.objs {
		if !o.delta {
			ready = append(ready, o)
			continue
		}
		pending++
		if o.baseID != "" {
			waitID[o.baseID] = append(waitID[o.baseID], o)
		} else {
			if _, ok := byOff[o.baseOff]; !ok {
				return protocolf("bad delta base")
			}
			waitOff[o.baseOff] = append(waitOff[o.baseOff], o)
		}
	}
	offOf := make(map[*object]int64, len(byOff))
	for off, o := range byOff {
		offOf[o] = off
	}
	w := bufio.NewWriterSize(s.f, 256<<10)
	if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	for len(ready) > 0 {
		base := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		children := append(waitOff[offOf[base]], waitID[base.id]...)
		delete(waitOff, offOf[base])
		delete(waitID, base.id)
		if len(children) == 0 {
			continue
		}
		if err := w.Flush(); err != nil {
			return err
		}
		baseData, err := s.data(base)
		if err != nil {
			return err
		}
		for _, d := range children {
			if err := w.Flush(); err != nil {
				return err
			}
			delta, err := s.data(d)
			if err != nil {
				return err
			}
			out, err := applyDelta(baseData, delta, s.lim.ObjectBytes)
			if err != nil {
				return err
			}
			if s.written+int64(len(out)) > s.lim.StoreBytes {
				return limitf("the repository is larger than %s unpacked", byteSize(s.lim.StoreBytes))
			}
			if _, err := w.Write(out); err != nil {
				return err
			}
			d.typ, d.off, d.size, d.delta = base.typ, s.written, int64(len(out)), false
			s.written += int64(len(out))
			h := sha1.New()
			h.Write([]byte(typeName(d.typ) + " " + strconv.Itoa(len(out)) + "\x00"))
			h.Write(out)
			d.id = hex.EncodeToString(h.Sum(nil))
			if _, dup := s.byID[d.id]; !dup {
				s.byID[d.id] = d
			}
			pending--
			ready = append(ready, d)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if pending > 0 {
		return protocolf("the pack refers to objects it does not contain")
	}
	return nil
}

// applyDelta builds an object from base and a delta (Git's format: the
// sizes of base and result, then copy and insert instructions).
func applyDelta(base, delta []byte, max int64) ([]byte, error) {
	bad := protocolf("bad delta")
	readSize := func() (int64, bool) {
		var v int64
		var shift uint
		for {
			if len(delta) == 0 || shift > 56 {
				return 0, false
			}
			b := delta[0]
			delta = delta[1:]
			v |= int64(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				return v, true
			}
		}
	}
	srcSize, ok := readSize()
	if !ok || srcSize != int64(len(base)) {
		return nil, bad
	}
	dstSize, ok := readSize()
	if !ok {
		return nil, bad
	}
	if dstSize > max {
		return nil, limitf("the repository contains a file or object larger than %s", byteSize(max))
	}
	out := make([]byte, 0, dstSize)
	for len(delta) > 0 {
		op := delta[0]
		delta = delta[1:]
		switch {
		case op&0x80 != 0:
			var off, n int64
			for i := range 4 {
				if op&(1<<i) != 0 {
					if len(delta) == 0 {
						return nil, bad
					}
					off |= int64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			for i := range 3 {
				if op&(0x10<<i) != 0 {
					if len(delta) == 0 {
						return nil, bad
					}
					n |= int64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			if n == 0 {
				n = 0x10000
			}
			if off+n > int64(len(base)) || int64(len(out))+n > dstSize {
				return nil, bad
			}
			out = append(out, base[off:off+n]...)
		case op != 0:
			n := int64(op)
			if n > int64(len(delta)) || int64(len(out))+n > dstSize {
				return nil, bad
			}
			out = append(out, delta[:n]...)
			delta = delta[n:]
		default:
			return nil, bad
		}
	}
	if int64(len(out)) != dstSize {
		return nil, bad
	}
	return out, nil
}

// get returns the object with id, if the pack had it.
func (s *packStore) get(id string) (*object, bool) {
	o, ok := s.byID[id]
	return o, ok
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return strconv.FormatInt(n>>30, 10) + " GB"
	case n >= 1<<20:
		return strconv.FormatInt(n>>20, 10) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
