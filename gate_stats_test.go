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

//go:build vellumstats

package vellum

import (
	"testing"

	"github.com/blevesearch/vellum/regexp"
)

// TestGateGetPhase13 is the Phase 1.2 -> 1.3 gate measurement from the
// zapx rewrite plan: run vellumstats -tags, run this test, and read the
// edgesVisited/distinctPairs ratio it logs. >= 3 justifies dead-subtree
// memoization (Phase 1.3); < 2 means the FST's suffix-sharing isn't dense
// enough for a memo to pay for its own probe cost.
func TestGateGetPhase13(t *testing.T) {
	for _, vocabPath := range []string{"testdata/vocab-small.txt", "testdata/vocab-realistic.txt"} {
		fst := buildVocabFST(t, vocabPath)
		aut, err := regexp.New(".*ing")
		if err != nil {
			t.Fatalf("regexp.New: %v", err)
		}

		StatsReset()
		itr, err := fst.Search(aut, nil, nil)
		n := drain(t, itr, err)
		snap := StatsGet()

		var ratio float64
		if snap.DistinctPairs > 0 {
			ratio = float64(snap.EdgesVisited) / float64(snap.DistinctPairs)
		}
		t.Logf("%s: %d matches, edgesVisited=%d pairsPushed=%d distinctPairs=%d ratio(edges/distinct)=%.2f",
			vocabPath, n, snap.EdgesVisited, snap.PairsPushed, snap.DistinctPairs, ratio)
	}
}
