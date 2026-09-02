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
	"fmt"
)

// Iterator represents a means of visiting key/value pairs in order.
type Iterator interface {

	// Current() returns the key/value pair currently pointed to.
	// The []byte of the key is ONLY guaranteed to be valid until
	// another call to Next/Seek/Close.  If you need it beyond that
	// point you MUST make a copy.
	Current() ([]byte, uint64)

	// Next() advances the iterator to the next key/value pair.
	// If no more key/value pairs exist, ErrIteratorDone is returned.
	Next() error

	// Seek() advances the iterator the specified key, or the next key
	// if it does not exist.
	// If no keys exist after that point, ErrIteratorDone is returned.
	Seek(key []byte) error

	// Reset resets the Iterator' internal state to allow for iterator
	// reuse (e.g. pooling).
	Reset(f *FST, startKeyInclusive, endKeyExclusive []byte, aut Automaton) error

	// Close() frees any resources held by this iterator.
	Close() error
}

type FuzzyIterator interface {
	Iterator
	EditDistance() uint8
}

// FSTIterator is a structure for iterating key/value pairs in this FST in
// lexicographic order.  Iterators should be constructed with the FSTIterator
// method on the parent FST structure.
//
// statesStack holds fstStateV1 VALUES, not the fstState interface - decoderV1
// is the only registered decoder, so the interface/registry machinery in
// encoding.go (kept for FST.Get/Debug and any future decoder version) buys
// this hot loop nothing but per-visit interface boxing and 3-5 avoidable
// dynamic dispatches per edge. dv1 is resolved once, in Reset, rather than
// type-asserted on every state visit.
type FSTIterator struct {
	f   *FST
	dv1 *decoderV1
	aut Automaton

	startKeyInclusive []byte
	endKeyExclusive   []byte

	statesStack    []fstStateV1
	keysStack      []byte
	keysPosStack   []int
	valsStack      []uint64
	autStatesStack []int

	nextStart []byte

	editDistance uint8
}

// pushState decodes the state at addr into a freshly appended slot in
// statesStack and returns a pointer to it. Appending a VALUE (not a pointer
// or interface) lets append's normal capacity-reuse do what the old
// nextPrealloc dance did explicitly, with no boxing - fstStateV1.at is
// total for every field any read method can access for its outcome (see
// atSingle/atMulti/atZero/atNone's doc comments in decoder_v1.go), so
// decoding into reused backing-array memory is exactly as safe as it was
// through decoder.stateAt's old prealloc parameter.
func (i *FSTIterator) pushState(addr int) (*fstStateV1, error) {
	i.statesStack = append(i.statesStack, fstStateV1{})
	s := &i.statesStack[len(i.statesStack)-1]
	if err := s.at(i.dv1.data, addr); err != nil {
		i.statesStack = i.statesStack[:len(i.statesStack)-1]
		return nil, err
	}
	return s, nil
}

func newIterator(f *FST, startKeyInclusive, endKeyExclusive []byte,
	aut Automaton) (*FSTIterator, error) {

	rv := &FSTIterator{}
	err := rv.Reset(f, startKeyInclusive, endKeyExclusive, aut)
	if err != nil {
		return nil, err
	}
	return rv, nil
}

func (i *FSTIterator) EditDistance() uint8 {
	return i.editDistance
}

// Reset resets the Iterator' internal state to allow for iterator
// reuse (e.g. pooling).
func (i *FSTIterator) Reset(f *FST,
	startKeyInclusive, endKeyExclusive []byte, aut Automaton) error {
	dv1, ok := f.decoder.(*decoderV1)
	if !ok {
		return fmt.Errorf("vellum: FSTIterator requires a v1 decoder, got %T", f.decoder)
	}

	if aut == nil {
		aut = alwaysMatchAutomaton
	}

	i.f = f
	i.dv1 = dv1
	i.startKeyInclusive = startKeyInclusive
	i.endKeyExclusive = endKeyExclusive
	i.aut = aut

	return i.pointTo(startKeyInclusive)
}

// pointTo attempts to point us to the specified location
func (i *FSTIterator) pointTo(key []byte) error {
	// tried to seek before start
	if bytes.Compare(key, i.startKeyInclusive) < 0 {
		key = i.startKeyInclusive
	}

	// tried to see past end
	if i.endKeyExclusive != nil &&
		bytes.Compare(key, i.endKeyExclusive) > 0 {
		key = i.endKeyExclusive
	}

	// reset any state, pointTo always starts over
	i.statesStack = i.statesStack[:0]
	i.keysStack = i.keysStack[:0]
	i.keysPosStack = i.keysPosStack[:0]
	i.valsStack = i.valsStack[:0]
	i.autStatesStack = i.autStatesStack[:0]

	curr, err := i.pushState(i.dv1.getRoot())
	if err != nil {
		return err
	}

	autStart := i.aut.Start()

	maxQ := -1
	// root is always part of the path
	i.autStatesStack = append(i.autStatesStack, autStart)
	for j := 0; j < len(key); j++ {
		keyJ := key[j]
		autCurr := i.autStatesStack[len(i.autStatesStack)-1]

		pos, nextAddr, nextVal := curr.TransitionFor(keyJ)
		if nextAddr == noneAddr {
			// needed transition doesn't exist
			// find last trans before the one we needed
			for q := curr.NumTransitions() - 1; q >= 0; q-- {
				if curr.TransitionAt(q) < keyJ {
					maxQ = q
					break
				}
			}
			break
		}
		autNext := i.aut.Accept(autCurr, keyJ)

		next, err := i.pushState(nextAddr)
		if err != nil {
			return err
		}

		i.keysStack = append(i.keysStack, keyJ)
		i.keysPosStack = append(i.keysPosStack, pos)
		i.valsStack = append(i.valsStack, nextVal)
		i.autStatesStack = append(i.autStatesStack, autNext)
		curr = next
		continue
	}

	if !i.statesStack[len(i.statesStack)-1].Final() ||
		!i.aut.IsMatch(i.autStatesStack[len(i.autStatesStack)-1]) ||
		bytes.Compare(i.keysStack, key) < 0 {
		return i.next(maxQ)
	}

	return nil
}

// Current returns the key and value currently pointed to by the iterator.
// If the iterator is not pointing at a valid value (because Iterator/Next/Seek)
// returned an error previously, it may return nil,0.
func (i *FSTIterator) Current() ([]byte, uint64) {
	curr := &i.statesStack[len(i.statesStack)-1]
	if curr.Final() {
		var total uint64
		for _, v := range i.valsStack {
			total += v
		}
		total += curr.FinalOutput()
		return i.keysStack, total
	}
	return nil, 0
}

// Next advances this iterator to the next key/value pair.  If there is none
// or the advancement goes beyond the configured endKeyExclusive, then
// ErrIteratorDone is returned.
func (i *FSTIterator) Next() error {
	return i.next(-1)
}

func (i *FSTIterator) next(lastOffset int) error {
	// remember where we started with keysStack in this next() call
	i.nextStart = append(i.nextStart[:0], i.keysStack...)

	nextOffset := lastOffset + 1
	allowCompare := false

OUTER:
	for true {
		curr := &i.statesStack[len(i.statesStack)-1]
		autCurr := i.autStatesStack[len(i.autStatesStack)-1]

		if curr.Final() && i.aut.IsMatch(autCurr) && allowCompare {
			// check to see if new keystack might have gone too far
			if i.endKeyExclusive != nil &&
				bytes.Compare(i.keysStack, i.endKeyExclusive) >= 0 {
				return ErrIteratorDone
			}

			cmp := bytes.Compare(i.keysStack, i.nextStart)
			if cmp > 0 {
				if fa, ok := i.aut.(FuzzyAutomaton); ok {
					i.editDistance = fa.EditDistance(autCurr)
				}
				// in final state greater than start key
				return nil
			}
		}

		numTrans := curr.NumTransitions()

	INNER:
		for nextOffset < numTrans {
			statsEdgeVisited()
			t := curr.TransitionAt(nextOffset)

			autNext := i.aut.Accept(autCurr, t)
			if !i.aut.CanMatch(autNext) {
				// TODO: potential optimization to skip nextOffset
				// forwards more directly to something that the
				// automaton likes rather than a linear scan?
				nextOffset += 1
				continue INNER
			}

			// nextOffset (i.e. the position TransitionFor would recompute
			// via a linear IndexByte scan) is already known here - it's
			// exactly what we just fetched t from via TransitionAt - so
			// decode the dest/output directly instead.
			nextAddr, v := curr.TransitionDestAt(nextOffset)
			statsPairPushed(nextAddr, autNext)

			if _, err := i.pushState(nextAddr); err != nil {
				return err
			}

			i.keysStack = append(i.keysStack, t)
			i.keysPosStack = append(i.keysPosStack, nextOffset)
			i.valsStack = append(i.valsStack, v)
			i.autStatesStack = append(i.autStatesStack, autNext)

			nextOffset = 0
			allowCompare = true

			continue OUTER
		}

		// no more transitions, so need to backtrack and stack pop
		if len(i.statesStack) <= 1 {
			// stack len is 1 (root), can't go back further, we're done
			break
		}

		// if the top of the stack represents a linear chain of states
		// (i.e., a suffix of nodes linked by single transitions),
		// then optimize by popping the suffix in one shot without
		// going back all the way to the OUTER loop
		var popNum int
		for j := len(i.statesStack) - 1; j > 0; j-- {
			if j == 1 || i.statesStack[j].NumTransitions() != 1 {
				popNum = len(i.statesStack) - 1 - j
				break
			}
		}
		if popNum < 1 { // always pop at least 1 entry from the stacks
			popNum = 1
		}

		nextOffset = i.keysPosStack[len(i.keysPosStack)-popNum] + 1
		allowCompare = false

		i.statesStack = i.statesStack[:len(i.statesStack)-popNum]
		i.keysStack = i.keysStack[:len(i.keysStack)-popNum]
		i.keysPosStack = i.keysPosStack[:len(i.keysPosStack)-popNum]
		i.valsStack = i.valsStack[:len(i.valsStack)-popNum]
		i.autStatesStack = i.autStatesStack[:len(i.autStatesStack)-popNum]
	}

	return ErrIteratorDone
}

// Seek advances this iterator to the specified key/value pair.  If this key
// is not in the FST, Current() will return the next largest key.  If this
// seek operation would go past the last key, or outside the configured
// startKeyInclusive/endKeyExclusive then ErrIteratorDone is returned.
func (i *FSTIterator) Seek(key []byte) error {
	return i.pointTo(key)
}

// Close will free any resources held by this iterator.
func (i *FSTIterator) Close() error {
	// at the moment we don't do anything,
	// but wanted this for API completeness
	return nil
}
