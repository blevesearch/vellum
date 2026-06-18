package vellum

import (
	"bytes"
	"testing"
)

// buildTestFST builds a small FST from sorted keys with val = len(key).
func buildTestFST(t *testing.T, keys []string) *FST {
	t.Helper()
	var buf bytes.Buffer
	b, err := New(&buf, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := b.Insert([]byte(k), uint64(len(k))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	fst, err := Load(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return fst
}

// walk consumes key one byte at a time, returning the accumulated output and
// whether the final state matched. When rs != nil it uses the reusable-state
// variants; otherwise the stock ones.
func walk(fst *FST, key string, rs *ReusableState) (uint64, bool) {
	addr := fst.Start()
	var sum uint64
	for i := 0; i < len(key); i++ {
		var next int
		var out uint64
		if rs != nil {
			next, out = fst.AcceptWithValState(addr, key[i], rs)
		} else {
			next, out = fst.AcceptWithVal(addr, key[i])
		}
		if next == noneAddr {
			return 0, false
		}
		addr = next
		sum += out
	}
	var final bool
	var fout uint64
	if rs != nil {
		final, fout = fst.IsMatchWithValState(addr, rs)
	} else {
		final, fout = fst.IsMatchWithVal(addr)
	}
	return sum + fout, final
}

// TestReusableStateMatchesStock verifies the *State variants return identical
// results to AcceptWithVal/IsMatchWithVal for matching, prefix, and absent keys.
func TestReusableStateMatchesStock(t *testing.T) {
	fst := buildTestFST(t, []string{"cat", "cats", "dog", "doge"})
	rs := NewReusableState()
	for _, k := range []string{"cat", "cats", "dog", "doge", "ca", "do", "zzz", "catx"} {
		wantVal, wantFinal := walk(fst, k, nil)
		gotVal, gotFinal := walk(fst, k, rs) // reuse rs across keys on purpose
		if gotVal != wantVal || gotFinal != wantFinal {
			t.Errorf("%q: state walk (%d,%v) != stock (%d,%v)", k, gotVal, gotFinal, wantVal, wantFinal)
		}
	}
}

// TestReusableStateNoAlloc asserts the reusable-state walk does not allocate per
// transition (the whole point of the API).
func TestReusableStateNoAlloc(t *testing.T) {
	fst := buildTestFST(t, []string{"cat", "cats", "dog", "doge"})
	rs := NewReusableState()
	allocs := testing.AllocsPerRun(200, func() {
		walk(fst, "cats", rs)
	})
	if allocs != 0 {
		t.Errorf("reusable-state walk allocated %v objects/run, want 0", allocs)
	}
}
