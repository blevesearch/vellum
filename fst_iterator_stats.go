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

// Package-level instrumentation for the Phase 1.2 -> 1.3 gate decision in
// the zapx rewrite plan (~/.claude/plans/lexical-baking-fairy.md): is the
// FST's suffix-sharing dense enough that memoizing dead (fstAddr, autState)
// subtrees would actually prune real work, or would the memo probe cost
// more than a scan-heavy automaton like ".*foo" ever revisits?
//
// Built only under the "vellumstats" tag so the production build (and
// every other benchmark/test in this package) pays zero cost - these
// globals are not safe for concurrent iterators, which is fine since this
// is a single-threaded gate measurement, not a runtime feature.
package vellum

var statsEdgesVisited uint64
var statsPairsPushed uint64
var statsDistinctPairs = map[uint64]struct{}{}

// statsResetCounters clears all counters, called once before each
// gate-measurement run (e.g. once per automaton walk being measured).
func statsResetCounters() {
	statsEdgesVisited = 0
	statsPairsPushed = 0
	statsDistinctPairs = map[uint64]struct{}{}
}

// statsEdgeVisited is called once per INNER-loop iteration in
// FSTIterator.next, whether or not the automaton accepts the byte -
// this is the traversal's total edge-examination cost.
func statsEdgeVisited() {
	statsEdgesVisited++
}

// statsPairPushed is called once per state actually decoded and pushed
// (i.e. an edge the automaton accepted), recording whether this
// (fstAddr, autState) pair has been seen before in this run. The pair is
// packed into a single uint64 - addr fits in 44 bits for any FST under
// ~17TB, autState in the remaining 20 bits (regexp/Levenshtein automata
// have nowhere near 2^20 states); overflow just degrades to
// undercounting distinct pairs, which only makes the gate more
// conservative, never falsely justifies memoization.
func statsPairPushed(addr, autState int) {
	statsPairsPushed++
	key := uint64(addr)<<20 | uint64(uint32(autState)&0xFFFFF)
	statsDistinctPairs[key] = struct{}{}
}

// StatsSnapshot is the gate measurement's output: edge visits vs. distinct
// (fstAddr, autState) pairs actually pushed. The plan's threshold is
// edgesVisited/distinctPairs >= 3 to justify Phase 1.3's memoization; < 2
// means the FST's suffix-sharing isn't dense enough for a memo to pay for
// its own probe cost.
type StatsSnapshot struct {
	EdgesVisited  uint64
	PairsPushed   uint64
	DistinctPairs uint64
}

func StatsReset() {
	statsResetCounters()
}

func StatsGet() StatsSnapshot {
	return StatsSnapshot{
		EdgesVisited:  statsEdgesVisited,
		PairsPushed:   statsPairsPushed,
		DistinctPairs: uint64(len(statsDistinctPairs)),
	}
}
