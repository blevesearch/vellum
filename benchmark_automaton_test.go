//  Copyright (c) 2026 Couchbase, Inc.
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

// BenchmarkAutomatonWalk isolates FST traversal cost from the rest of the
// bleve/zapx query stack - Phase 1.0 of the zapx rewrite plan
// (~/.claude/plans/lexical-baking-fairy.md). End-to-end query benchmarks
// have repeatedly proven too diluted to iterate against directly (this
// investigation has more than once found a stage the profiler attributed
// 18%+ of query time to yield only ~2% wall-clock when optimized away), so
// this gives a tight ns/op signal to develop Phase 1.1-1.3 against, with
// the full bench-optim harness as the end-to-end confirmation afterward,
// not the primary iteration loop.
//
// testdata/vocab-realistic.txt (~1.24M distinct terms, dumped via
// bleve-optim/bench's dumpvocab tool from a corpus augmented by
// genbigvocab) is the realistic-scale case; testdata/vocab-small.txt
// (~20K terms, the original bench corpus before that fix) is kept as a
// control specifically because it's small enough to plausibly fit in L2/L3
// cache - cache-locality wins (the point of Phase 1.1's decoder changes)
// should show up as a much bigger delta on the realistic file than on this
// one, and a change that only helps the small file is a warning sign, not
// a result.

import (
	"bufio"
	"bytes"
	"os"
	"testing"

	"github.com/blevesearch/vellum/levenshtein"
	"github.com/blevesearch/vellum/regexp"
)

func loadVocab(tb testing.TB, path string) []string {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Skipf("open %s: %v (local-only benchmark data: run bleve-optim/bench's dumpvocab tool to regenerate)", path, err)
	}
	defer f.Close()

	var words []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		words = append(words, sc.Text())
	}
	if err := sc.Err(); err != nil {
		tb.Fatalf("scan %s: %v", path, err)
	}
	return words
}

// buildVocabFST builds an FST from a vocabulary file's terms (must already
// be sorted - dumpvocab guarantees this), with each term mapped to its own
// ordinal so per-key values are distinct and meaningless beyond that.
func buildVocabFST(tb testing.TB, path string) *FST {
	tb.Helper()
	words := loadVocab(tb, path)

	var buf bytes.Buffer
	b, err := New(&buf, nil)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	for i, w := range words {
		if err := b.Insert([]byte(w), uint64(i)); err != nil {
			tb.Fatalf("Insert(%q): %v (vocab file must be sorted, deduped)", w, err)
		}
	}
	if err := b.Close(); err != nil {
		tb.Fatalf("Close: %v", err)
	}

	fst, err := Load(buf.Bytes())
	if err != nil {
		tb.Fatalf("Load: %v", err)
	}
	tb.Logf("built FST from %s: %d terms, %d bytes", path, len(words), len(buf.Bytes()))
	return fst
}

// drain fully exhausts an iterator, matching how bleve's regexp/fuzzy
// searchers consume a dictionary iterator (search_regexp.go/search_fuzzy.go
// both drain to completion eagerly before doing anything with the results).
func drain(tb testing.TB, itr *FSTIterator, err error) int {
	tb.Helper()
	n := 0
	for err == nil {
		n++
		err = itr.Next()
	}
	if err != ErrIteratorDone {
		tb.Fatalf("iterator error: %v", err)
	}
	return n
}

func benchmarkSuffixWalk(b *testing.B, vocabPath string) {
	fst := buildVocabFST(b, vocabPath)
	// bleve's wildcardRegexpReplacer turns "*ing" into the regex ".*ing"
	// (search/query/wildcard.go); parseRegexp's literalPrefix walk
	// (index/scorch/regexp.go) returns "" for a root .* node, so
	// startKeyInclusive/endKeyExclusive are nil - zero pruning, exactly
	// reproduced here rather than approximated.
	aut, err := regexp.New(".*ing")
	if err != nil {
		b.Fatalf("regexp.New: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		itr, err := fst.Search(aut, nil, nil)
		n := drain(b, itr, err)
		if i == 0 {
			b.Logf("%d matches", n)
		}
	}
}

func BenchmarkSuffixWalkRealistic(b *testing.B) {
	benchmarkSuffixWalk(b, "testdata/vocab-realistic.txt")
}

func BenchmarkSuffixWalkSmall(b *testing.B) {
	benchmarkSuffixWalk(b, "testdata/vocab-small.txt")
}

func benchmarkLevenshteinWalk(b *testing.B, vocabPath string) {
	fst := buildVocabFST(b, vocabPath)
	lb, err := levenshtein.NewLevenshteinAutomatonBuilder(2, false)
	if err != nil {
		b.Fatalf("NewLevenshteinAutomatonBuilder: %v", err)
	}
	dfa, err := lb.BuildDfa("information", 2)
	if err != nil {
		b.Fatalf("BuildDfa: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		itr, err := fst.Search(dfa, nil, nil)
		n := drain(b, itr, err)
		if i == 0 {
			b.Logf("%d matches", n)
		}
	}
}

func BenchmarkLevenshteinWalkRealistic(b *testing.B) {
	benchmarkLevenshteinWalk(b, "testdata/vocab-realistic.txt")
}

func BenchmarkLevenshteinWalkSmall(b *testing.B) {
	benchmarkLevenshteinWalk(b, "testdata/vocab-small.txt")
}
