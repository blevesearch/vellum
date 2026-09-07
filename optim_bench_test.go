//  Copyright (c) 2017 Couchbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 		http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vellum

import (
	"bytes"
	"sort"
	"testing"

	"github.com/blevesearch/vellum/levenshtein"
	"github.com/blevesearch/vellum/regexp"
)

// genWideKeys produces n deterministic, sorted, unique keys with a wide byte
// distribution, approximating a term dictionary of ids / hashes / tokens where
// the root and near-root FST states have very high fanout.
func genWideKeys(n, keyLen int) [][]byte {
	keys := make([][]byte, 0, n)
	seen := make(map[string]struct{}, n)
	var state uint64 = 0x9e3779b97f4a7c15
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	for len(keys) < n {
		k := make([]byte, keyLen)
		for j := 0; j < keyLen; j++ {
			k[j] = byte(next()%255) + 1
		}
		if _, ok := seen[string(k)]; ok {
			continue
		}
		seen[string(k)] = struct{}{}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}

func benchWordKeys(tb testing.TB) [][]byte {
	words, err := loadWords("data/words-1000.txt")
	if err != nil {
		tb.Fatal(err)
	}
	sort.Strings(words)
	keys := make([][]byte, len(words))
	for i, w := range words {
		keys[i] = []byte(w)
	}
	return keys
}

// buildBenchFST builds an FST over the (sorted, unique) keys using the default
// builder options.
func buildBenchFST(tb testing.TB, keys [][]byte) []byte {
	var buf bytes.Buffer
	b, err := New(&buf, nil)
	if err != nil {
		tb.Fatal(err)
	}
	for i, k := range keys {
		if err := b.Insert(k, uint64(i)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := b.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

func benchGet(b *testing.B, keys [][]byte) {
	fst, err := Load(buildBenchFST(b, keys))
	if err != nil {
		b.Fatal(err)
	}
	defer fst.Close()
	r, err := fst.Reader()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var sink uint64
	for i := 0; i < b.N; i++ {
		for _, k := range keys {
			v, _, _ := r.Get(k)
			sink += v
		}
	}
	_ = sink
}

func benchScan(b *testing.B, keys [][]byte) {
	fst, err := Load(buildBenchFST(b, keys))
	if err != nil {
		b.Fatal(err)
	}
	defer fst.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		itr, err := fst.Iterator(nil, nil)
		for err == nil {
			_, _ = itr.Current()
			err = itr.Next()
		}
	}
}

func benchFuzzy(b *testing.B, keys [][]byte, query string, dist uint8) {
	fst, err := Load(buildBenchFST(b, keys))
	if err != nil {
		b.Fatal(err)
	}
	defer fst.Close()
	lb, err := levenshtein.NewLevenshteinAutomatonBuilder(dist, false)
	if err != nil {
		b.Fatal(err)
	}
	dfa, err := lb.BuildDfa(query, dist)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		itr, err := fst.Search(dfa, nil, nil)
		for err == nil {
			_, _ = itr.Current()
			err = itr.Next()
		}
	}
}

func benchRegex(b *testing.B, keys [][]byte, expr string) {
	fst, err := Load(buildBenchFST(b, keys))
	if err != nil {
		b.Fatal(err)
	}
	defer fst.Close()
	r, err := regexp.New(expr)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		itr, err := fst.Search(r, nil, nil)
		for err == nil {
			_, _ = itr.Current()
			err = itr.Next()
		}
	}
}

// Exact lookups (Reader.Get) - exercises the cached root state.
func BenchmarkOptGetWords(b *testing.B) { benchGet(b, benchWordKeys(b)) }
func BenchmarkOptGetWide(b *testing.B)  { benchGet(b, genWideKeys(50000, 8)) }
func BenchmarkOptGetWide2(b *testing.B) { benchGet(b, genWideKeys(60000, 2)) }

// Full range scan - exercises the iterator offset accessor.
func BenchmarkOptScanWords(b *testing.B) { benchScan(b, benchWordKeys(b)) }
func BenchmarkOptScanWide(b *testing.B)  { benchScan(b, genWideKeys(50000, 8)) }

// Automaton-guided search.
func BenchmarkOptFuzzyWords1(b *testing.B) { benchFuzzy(b, benchWordKeys(b), "the", 1) }
func BenchmarkOptFuzzyWords2(b *testing.B) { benchFuzzy(b, benchWordKeys(b), "American", 2) }
func BenchmarkOptRegexWords(b *testing.B)  { benchRegex(b, benchWordKeys(b), ".*a.*e.*") }
