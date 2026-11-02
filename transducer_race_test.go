package vellum

import (
	"sync"
	"testing"
)

// Exercises the pooled Automaton/Transducer methods concurrently to ensure
// the statePool sharing is race-free.
func TestTransducerConcurrent(t *testing.T) {
	fst := buildSmallFST(t)
	keys := [][]byte{
		[]byte("application"), []byte("banana"), []byte("orchard"),
		[]byte("apply"), []byte("bandana"), []byte("ordinary"),
		[]byte("missing"), []byte("apple"),
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				k := keys[i%len(keys)]
				_, _ = TransducerGet(fst, k)
				// also drive the bare Automaton methods
				st := fst.Start()
				for _, b := range k {
					st = fst.Accept(st, b)
					if st == noneAddr {
						break
					}
				}
				if st != noneAddr {
					_ = fst.IsMatch(st)
				}
			}
		}()
	}
	wg.Wait()
}
