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

//go:build !vellumstats

// No-op counterparts to fst_iterator_stats.go's instrumentation, so the
// hooks in FSTIterator.next compile away to nothing (inlined empty calls)
// in every normal build.
package vellum

func statsEdgeVisited() {}

func statsPairPushed(addr, autState int) {}
