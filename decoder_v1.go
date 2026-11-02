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
	"encoding/binary"
	"fmt"
	"strconv"
)

func init() {
	registerDecoder(versionV1, func(data []byte) decoder {
		return newDecoderV1(data)
	})
}

type decoderV1 struct {
	data []byte
}

func newDecoderV1(data []byte) *decoderV1 {
	return &decoderV1{
		data: data,
	}
}

func (d *decoderV1) getRoot() int {
	if len(d.data) < footerSizeV1 {
		return noneAddr
	}
	footer := d.data[len(d.data)-footerSizeV1:]
	root := binary.LittleEndian.Uint64(footer[8:])
	return int(root)
}

func (d *decoderV1) getLen() int {
	if len(d.data) < footerSizeV1 {
		return 0
	}
	footer := d.data[len(d.data)-footerSizeV1:]
	dlen := binary.LittleEndian.Uint64(footer)
	return int(dlen)
}

func (d *decoderV1) stateAt(addr int, prealloc fstState) (fstState, error) {
	state, ok := prealloc.(*fstStateV1)
	if !ok || state == nil {
		state = &fstStateV1{}
	}
	// No struct-clear here: at()'s two branches (atSingle/atMulti) are each
	// total with respect to every field the rest of this type's methods
	// can actually read for that branch's f.single value - see the "total"
	// comments on atSingle/atMulti for exactly which field required this
	// and why a stale value would otherwise be observable across reuse.
	err := state.at(d.data, addr)
	if err != nil {
		return nil, err
	}
	return state, nil
}

type fstStateV1 struct {
	data     []byte
	top      int
	bottom   int
	numTrans int
	single   bool

	// single trans only
	singleTransChar byte
	singleTransNext bool
	singleTransAddr uint64
	singleTransOut  uint64

	// shared
	transSize int
	outSize   int

	// multiple trans only
	final       bool
	transTop    int
	transBottom int
	destTop     int
	destBottom  int
	outTop      int
	outBottom   int
	outFinal    int
}

func (f *fstStateV1) isEncodedSingle() bool {
	if f.data[f.top]>>7 > 0 {
		return true
	}
	return false
}

func (f *fstStateV1) at(data []byte, addr int) error {
	f.data = data
	if addr == emptyAddr {
		return f.atZero()
	} else if addr == noneAddr {
		return f.atNone()
	}
	if addr > len(data) || addr < 16 {
		return fmt.Errorf("invalid address %d/%d", addr, len(data))
	}
	f.top = addr
	f.bottom = addr
	// Read and cache once per visit instead of on every TransitionAt/
	// TransitionFor/TransitionDestAt call (each of those used to call
	// isEncodedSingle() itself - a re-read of f.data[f.top], often a
	// different cache line than the transition data those methods go on
	// to touch).
	f.single = f.isEncodedSingle()
	if f.single {
		return f.atSingle(data, addr)
	}
	return f.atMulti(data, addr)
}

// atZero and atNone both represent numTrans==0 ("no outgoing transitions")
// states, but none of TransitionAt/TransitionFor/TransitionDestAt actually
// gate on numTrans==0 - they unconditionally slice
// f.data[f.transBottom:f.transTop] (and destBottom:destTop, outBottom:
// outTop) and search within it, relying entirely on that slice being
// EMPTY (transBottom==transTop) for a numTrans==0 state to correctly find
// no match. A reused prealloc'd struct whose last decode was a real
// multi-transition state would otherwise leave these fields non-empty and
// stale, so TransitionFor could spuriously "find" an unrelated byte within
// that leftover range and compute a bogus destination address from
// whatever garbage sits at the corresponding (equally stale) destBottom:
// destTop position - not a hypothetical, this is exactly what produced a
// negative address surfacing as "invalid address" several calls later.
// outSize must also be cleared: FinalOutput() reads f.data[outFinal:
// outFinal+outSize] whenever final && outSize>0, and this path never
// otherwise sets outSize.
func (f *fstStateV1) atZero() error {
	f.top = 0
	f.bottom = 1
	f.numTrans = 0
	f.single = false
	f.final = true
	f.transBottom, f.transTop = 0, 0
	f.destBottom, f.destTop = 0, 0
	f.outBottom, f.outTop = 0, 0
	f.outFinal = 0
	f.outSize = 0
	return nil
}

func (f *fstStateV1) atNone() error {
	f.top = 0
	f.bottom = 1
	f.numTrans = 0
	f.single = false
	f.final = false
	f.transBottom, f.transTop = 0, 0
	f.destBottom, f.destTop = 0, 0
	f.outBottom, f.outTop = 0, 0
	f.outFinal = 0
	f.outSize = 0
	return nil
}

// atSingle is "total" with respect to every field this type's methods can
// read when f.single is true: numTrans, the singleTrans* fields below, and
// - the one easy to miss - final, which single-transition nodes can never
// truthfully have (the encoder always takes the many-transition path for
// any final node, see encoder_v1.go's encodeState), but which Final() reads
// unconditionally regardless of f.single. Without this, a state struct
// reused (via stateAt's prealloc) from a previous final multi-transition
// decode would leak a stale final=true into this single-transition one.
func (f *fstStateV1) atSingle(data []byte, addr int) error {
	// handle single transition case
	f.numTrans = 1
	f.final = false
	f.outFinal = 0
	f.singleTransNext = data[f.top]&transitionNext > 0
	f.singleTransChar = data[f.top] & maxCommon
	if f.singleTransChar == 0 {
		f.bottom-- // extra byte for uncommon
		f.singleTransChar = data[f.bottom]
	} else {
		f.singleTransChar = decodeCommon(f.singleTransChar)
	}
	if f.singleTransNext {
		// now we know the bottom, can compute next addr
		f.singleTransAddr = uint64(f.bottom - 1)
		f.singleTransOut = 0
	} else {
		f.bottom-- // extra byte with pack sizes
		f.transSize, f.outSize = decodePackSize(data[f.bottom])
		f.bottom -= f.transSize // exactly one trans
		f.singleTransAddr = readPackedUint(data[f.bottom : f.bottom+f.transSize])
		if f.outSize > 0 {
			f.bottom -= f.outSize // exactly one out (could be length 0 though)
			f.singleTransOut = readPackedUint(data[f.bottom : f.bottom+f.outSize])
		} else {
			f.singleTransOut = 0
		}
		// need to wait till we know bottom
		if f.singleTransAddr != 0 {
			f.singleTransAddr = uint64(f.bottom) - f.singleTransAddr
		}
	}
	return nil
}

func (f *fstStateV1) atMulti(data []byte, addr int) error {
	// handle multiple transitions case
	f.final = data[f.top]&stateFinal > 0
	f.numTrans = int(data[f.top] & maxNumTrans)
	if f.numTrans == 0 {
		f.bottom-- // extra byte for number of trans
		f.numTrans = int(data[f.bottom])
		if f.numTrans == 1 {
			// can't really be 1 here, this is special case that means 256
			f.numTrans = 256
		}
	}
	f.bottom-- // extra byte with pack sizes
	f.transSize, f.outSize = decodePackSize(data[f.bottom])

	f.transTop = f.bottom
	f.bottom -= f.numTrans // one byte for each transition
	f.transBottom = f.bottom

	f.destTop = f.bottom
	f.bottom -= f.numTrans * f.transSize
	f.destBottom = f.bottom

	if f.outSize > 0 {
		f.outTop = f.bottom
		f.bottom -= f.numTrans * f.outSize
		f.outBottom = f.bottom
		if f.final {
			f.bottom -= f.outSize
			f.outFinal = f.bottom
		} else {
			f.outFinal = 0
		}
	} else {
		// Total even when there's nothing to decode: TransitionFor takes
		// an unconditional f.data[f.outBottom:f.outTop] slice regardless
		// of outSize (only the subsequent index into it is guarded), so
		// stale bottom/top from this same struct's previous use - a
		// different FST, reused via Reset()'s iterator pooling, which
		// bleve's automaton-per-dictionary-lookup path (e.g. geo queries
		// constructing many small automatons) does heavily - could
		// otherwise violate len(f.data) and panic, or silently slice
		// nonsense out of the new, unrelated buffer.
		f.outTop = f.bottom
		f.outBottom = f.bottom
		f.outFinal = 0
	}
	return nil
}

func (f *fstStateV1) Address() int {
	return f.top
}

func (f *fstStateV1) Final() bool {
	return f.final
}

func (f *fstStateV1) FinalOutput() uint64 {
	if f.final && f.outSize > 0 {
		return readPackedUint(f.data[f.outFinal : f.outFinal+f.outSize])
	}
	return 0
}

func (f *fstStateV1) NumTransitions() int {
	return f.numTrans
}

func (f *fstStateV1) TransitionAt(i int) byte {
	if f.single {
		return f.singleTransChar
	}
	transitionKeys := f.data[f.transBottom:f.transTop]
	return transitionKeys[f.numTrans-i-1]
}

// TransitionDestAt returns the destination address and output value for
// the i'th transition in ascending byte order (the same i TransitionAt
// takes) - equivalent to the 2nd/3rd results of
// TransitionFor(TransitionAt(i)), but computes the position directly
// instead of re-deriving it via TransitionFor's bytes.IndexByte scan,
// which the caller (FSTIterator.next) already knows since it just got i
// from TransitionAt. Kept as a separate call from TransitionAt rather than
// merged into one, so a rejected edge (the common case for a
// well-pruning automaton like Levenshtein) doesn't pay for a dest/output
// decode it won't use.
func (f *fstStateV1) TransitionDestAt(i int) (int, uint64) {
	if f.single {
		return int(f.singleTransAddr), f.singleTransOut
	}
	pos := f.numTrans - i - 1
	transDests := f.data[f.destBottom:f.destTop]
	dest := int(readPackedUint(transDests[pos*f.transSize : pos*f.transSize+f.transSize]))
	if dest > 0 {
		dest = f.bottom - dest
	}
	var out uint64
	if f.outSize > 0 {
		transVals := f.data[f.outBottom:f.outTop]
		out = readPackedUint(transVals[pos*f.outSize : pos*f.outSize+f.outSize])
	}
	return dest, out
}

func (f *fstStateV1) TransitionFor(b byte) (int, int, uint64) {
	if f.single {
		if f.singleTransChar == b {
			return 0, int(f.singleTransAddr), f.singleTransOut
		}
		return -1, noneAddr, 0
	}
	transitionKeys := f.data[f.transBottom:f.transTop]
	pos := bytes.IndexByte(transitionKeys, b)
	if pos < 0 {
		return -1, noneAddr, 0
	}
	transDests := f.data[f.destBottom:f.destTop]
	dest := int(readPackedUint(transDests[pos*f.transSize : pos*f.transSize+f.transSize]))
	if dest > 0 {
		// convert delta
		dest = f.bottom - dest
	}
	transVals := f.data[f.outBottom:f.outTop]
	var out uint64
	if f.outSize > 0 {
		out = readPackedUint(transVals[pos*f.outSize : pos*f.outSize+f.outSize])
	}
	return f.numTrans - pos - 1, dest, out
}

func (f *fstStateV1) String() string {
	rv := ""
	rv += fmt.Sprintf("State: %d (%#x)", f.top, f.top)
	if f.final {
		rv += " final"
		fout := f.FinalOutput()
		if fout != 0 {
			rv += fmt.Sprintf(" (%d)", fout)
		}
	}
	rv += "\n"
	rv += fmt.Sprintf("Data: % x\n", f.data[f.bottom:f.top+1])

	for i := 0; i < f.numTrans; i++ {
		transChar := f.TransitionAt(i)
		_, transDest, transOut := f.TransitionFor(transChar)
		rv += fmt.Sprintf(" - %d (%#x) '%s' ---> %d (%#x)  with output: %d", transChar, transChar, string(transChar), transDest, transDest, transOut)
		rv += "\n"
	}
	if f.numTrans == 0 {
		rv += "\n"
	}
	return rv
}

func (f *fstStateV1) DotString(num int) string {
	rv := ""
	label := fmt.Sprintf("%d", num)
	final := ""
	if f.final {
		final = ",peripheries=2"
	}
	rv += fmt.Sprintf("    %d [label=\"%s\"%s];\n", f.top, label, final)

	for i := 0; i < f.numTrans; i++ {
		transChar := f.TransitionAt(i)
		_, transDest, transOut := f.TransitionFor(transChar)
		out := ""
		if transOut != 0 {
			out = fmt.Sprintf("/%d", transOut)
		}
		rv += fmt.Sprintf("    %d -> %d [label=\"%s%s\"];\n", f.top, transDest, escapeInput(transChar), out)
	}

	return rv
}

func escapeInput(b byte) string {
	x := strconv.AppendQuoteRune(nil, rune(b))
	return string(x[1:(len(x) - 1)])
}
