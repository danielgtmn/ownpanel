package gitsource

import (
	"bytes"
	"testing"
)

// The parsers get whatever a hostile server sends: they must fail, never
// panic or run past their limits.

func FuzzReadPack(f *testing.F) {
	f.Add([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"))
	f.Add([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01\x30\x78\x9c\x03\x00\x00\x00\x00\x01"))
	f.Add([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x02\x60\x05"))
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := DefaultLimits
		lim.StoreBytes, lim.ObjectBytes, lim.Objects, lim.TransferBytes = 1<<20, 1<<16, 100, 1<<20
		s, err := readPack(bytes.NewReader(data), t.TempDir(), lim)
		if err == nil {
			s.close()
		}
	})
}

func FuzzReadAdvert(f *testing.F) {
	var b bytes.Buffer
	writePkt(&b, "1111111111111111111111111111111111111111 HEAD\x00side-band-64k symref=HEAD:refs/heads/main\n")
	writePkt(&b, "1111111111111111111111111111111111111111 refs/heads/main\n")
	writeFlush(&b)
	f.Add(b.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		p := newPktReader(bytes.NewReader(data))
		if ad, err := readAdvert(p); err == nil {
			_ = fetchRequest(ad, "1111111111111111111111111111111111111111")
		}
		_ = readUntilPack(newPktReader(bytes.NewReader(data)))
	})
}

func FuzzApplyDelta(f *testing.F) {
	f.Add([]byte("hello world"), []byte{11, 5, 0x91, 0, 5})
	f.Fuzz(func(t *testing.T, base, delta []byte) {
		_, _ = applyDelta(base, delta, 1<<16)
	})
}
