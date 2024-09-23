//  Copyright (c) 2024 Couchbase, Inc.
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

import "io"

type SynonymAutomaton struct {
}

func NewSynonymAutomaton(data []byte, f io.Closer) (rv *SynonymAutomaton, err error) {
	return &SynonymAutomaton{}, nil
}

func (f *SynonymAutomaton) Get(val []byte) ([]uint64, error) {
	return nil, nil
}

func (f *SynonymAutomaton) Close() error {
	return nil
}

func (f *SynonymAutomaton) Merge(with *SynonymAutomaton) error {
	return nil
}
