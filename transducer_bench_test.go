package vellum

import (
	"bytes"
	"testing"
)

func buildSmallFST(tb testing.TB) *FST {
	var buf bytes.Buffer
	b, err := New(&buf, nil)
	if err != nil {
		tb.Fatal(err)
	}
	keys := [][]byte{
		[]byte("apple"), []byte("application"), []byte("apply"),
		[]byte("banana"), []byte("band"), []byte("bandana"),
		[]byte("orange"), []byte("orchard"), []byte("ordinary"),
	}
	for i, k := range keys {
		if err := b.Insert(k, uint64(i)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := b.Close(); err != nil {
		tb.Fatal(err)
	}
	fst, err := Load(buf.Bytes())
	if err != nil {
		tb.Fatal(err)
	}
	return fst
}

// generic Transducer path: AcceptWithVal per byte + IsMatchWithVal once
func BenchmarkTransducerGet(b *testing.B) {
	fst := buildSmallFST(b)
	key := []byte("application")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = TransducerGet(fst, key)
	}
}

// FST.Get (allocates one state per call)
func BenchmarkFSTGet(b *testing.B) {
	fst := buildSmallFST(b)
	key := []byte("application")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = fst.Get(key)
	}
}

// Reader.Get (reuses prealloc state)
func BenchmarkReaderGet(b *testing.B) {
	fst := buildSmallFST(b)
	r, _ := fst.Reader()
	key := []byte("application")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = r.Get(key)
	}
}
