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
	data    []byte
	outType int
}

func newDecoderV1(data []byte) *decoderV1 {
	return &decoderV1{
		data:    data,
		outType: int(binary.LittleEndian.Uint64(data[8:])),
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
	if ok && state != nil {
		*state = fstStateV1{} // clear the struct
	} else {
		state = &fstStateV1{}
	}
	state.outType = d.outType
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
	outType  int

	// single trans only
	singleTransChar byte        //key of trans
	singleTransNext bool        // if there is a next trans
	singleTransAddr uint64      // trans's destination state addr
	singleTransOut  interface{} // trans output

	// shared
	transSize        int
	outSize          int
	outSizesPackSize int

	// in case of []int output type
	// the problem with using fixed size
	// outSize, transSize is that we might end up
	// padding a lot according to the biggest
	// transVal.
	//
	// ideally we would need to have something like
	// array of transitions and array of outputs,
	// each of whose elements corresponding to specific
	// transition's properties such as the size of output
	// this would reduce the space consumption.
	// multiple trans only
	final          bool
	transTop       int
	transBottom    int
	destTop        int
	destBottom     int
	outTop         int
	outBottom      int
	outSizesTop    int
	outSizesBottom int
	outFinal       int
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

	// the following two functions essentially populate
	// the fstState with values such as outSize,
	// transSize, singleTransOut etc.
	// in case of atSingle we directly get the output
	// of the node at that address, but if the node
	// has multiple outputs then we would populate
	// certain fields of the fstState and then later
	// on get each transition output etc as and when
	// needed.
	if f.isEncodedSingle() {
		return f.atSingle(data, addr)
	}
	return f.atMulti(data, addr)
}

func (f *fstStateV1) atZero() error {
	f.top = 0
	f.bottom = 1
	f.numTrans = 0
	f.final = true
	f.outFinal = 0
	return nil
}

func (f *fstStateV1) atNone() error {
	f.top = 0
	f.bottom = 1
	f.numTrans = 0
	f.final = false
	f.outFinal = 0
	return nil
}

func (f *fstStateV1) zeroOutput() interface{} {
	switch f.outType {
	case storeIntSlice:
		return []uint64{}
	default:
		return 0
	}
}

func (f *fstStateV1) outputIsIntSlice() bool {
	switch f.outType {
	case storeIntSlice:
		return true
	default:
		return false
	}
}

func (f *fstStateV1) atSingle(data []byte, addr int) error {
	// handle single transition case
	f.numTrans = 1
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
		f.singleTransOut = f.zeroOutput()
	} else {
		f.bottom-- // extra byte with pack sizes
		f.transSize, f.outSize = decodePackSize(data[f.bottom])
		f.bottom -= f.transSize // exactly one trans
		f.singleTransAddr = readPackedUint(data[f.bottom : f.bottom+f.transSize])
		if f.outSize > 0 {
			f.bottom -= f.outSize // exactly one out (could be length 0 though)
			f.singleTransOut = f.readOutput(data[f.bottom : f.bottom+f.outSize])
		} else {
			f.singleTransOut = f.zeroOutput()
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
	if f.outputIsIntSlice() {
		f.outSizesPackSize = f.outSize
	}
	f.transTop = f.bottom
	f.bottom -= f.numTrans // one byte for each transition
	f.transBottom = f.bottom

	f.destTop = f.bottom
	f.bottom -= f.numTrans * f.transSize
	f.destBottom = f.bottom

	// this would correspond to the outSizes array
	// the elements of which would correspond to a
	// transition outputs size.
	if f.outputIsIntSlice() && f.outSizesPackSize > 0 {
		f.outSizesTop = f.bottom
		f.bottom -= f.numTrans * f.outSizesPackSize
		f.outSizesBottom = f.bottom

		transValsSize := f.transValPos(f.data[f.outSizesBottom:f.outSizesTop], f.numTrans)
		f.outTop = f.bottom
		f.bottom -= transValsSize
		f.outBottom = f.bottom
		if f.final {
			finalOutputSize := f.transValPos(f.data[f.outSizesTop:f.outSizesTop+f.outSizesPackSize], 1)
			f.bottom -= finalOutputSize
			f.outFinal = f.bottom
		}
		return nil
	}

	if f.outSize > 0 {
		f.outTop = f.bottom
		f.bottom -= f.numTrans * f.outSize
		f.outBottom = f.bottom
		if f.final {
			f.bottom -= f.outSize
			f.outFinal = f.bottom
		}
	}
	return nil
}

func (f *fstStateV1) Address() int {
	return f.top
}

func (f *fstStateV1) Final() bool {
	return f.final
}

func (f *fstStateV1) FinalOutput() interface{} {
	if f.final && f.outSize > 0 {
		return f.readOutput(f.data[f.outFinal : f.outFinal+f.outSize])
	}
	return f.zeroOutput()
}

func (f *fstStateV1) NumTransitions() int {
	return f.numTrans
}

func (f *fstStateV1) TransitionAt(i int) byte {
	if f.isEncodedSingle() {
		return f.singleTransChar
	}
	transitionKeys := f.data[f.transBottom:f.transTop]
	return transitionKeys[f.numTrans-i-1]
}

func (f *fstStateV1) readOutput(data []byte) interface{} {
	typ := f.outType
	switch typ {
	case storeIntSlice:
		// TODO: Need to process the int slice
		// not just return it.
		// Depends on the encoder format.
		return readPackedIntSlice(data)
	default:
		return readPackedUint(data)
	}
}

func (f *fstStateV1) transValPos(outSizes []byte, pos int) int {
	var rv int
	for i := 0; i < pos*f.outSizesPackSize; i += f.outSizesPackSize {
		rv += int(readPackedUint(outSizes[i : i+f.outSizesPackSize]))
	}
	return rv
}

func (f *fstStateV1) TransitionFor(b byte) (int, int, interface{}) {
	if f.isEncodedSingle() {
		if f.singleTransChar == b {
			return 0, int(f.singleTransAddr), f.singleTransOut
		}
		return -1, noneAddr, f.zeroOutput()
	}
	transitionKeys := f.data[f.transBottom:f.transTop]
	pos := bytes.IndexByte(transitionKeys, b)
	if pos < 0 {
		return -1, noneAddr, f.zeroOutput()
	}

	transDests := f.data[f.destBottom:f.destTop]
	dest := int(readPackedUint(transDests[pos*f.transSize : pos*f.transSize+f.transSize]))
	if dest > 0 {
		// convert delta
		dest = f.bottom - dest
	}

	// get like the transOuts
	if f.outputIsIntSlice() {
		outSizes := f.data[f.outSizesBottom:f.outSizesTop]
		outSize := int(readPackedUint(outSizes[pos*f.outSizesPackSize : pos*f.outSizesPackSize+f.outSizesPackSize]))

		transVals := f.data[f.outBottom:f.outTop]
		posOut := f.transValPos(outSizes, pos)
		out := f.readOutput(transVals[posOut : posOut+outSize])
		return f.numTrans - pos - 1, dest, out
	}

	// now how to traverse the transVals?
	//   we might have to get the starting offset of this byte 'b'
	// 	and set the pos with that
	// setPosVal := func(transOuts []int, pos int) int {
	// 	var rv int
	// 	for i := 0; i < pos*f.outSize; i += f.outSize {
	// 		rv += int(readPackedUint(transOuts[i:i+f.outSize]))
	// 	}
	// 	return rv
	// }
	// pos = setPosVal(transOuts, pos)
	// 	f.readOutput(transVals[pos : pos+out])
	//
	// would have to store the starting offsets??
	//  no need because we can do a cumulative sum of the prev vals
	// in the transOuts array.

	transVals := f.data[f.outBottom:f.outTop]
	var out interface{}
	if f.outSize > 0 {
		out = f.readOutput(transVals[pos*f.outSize : pos*f.outSize+f.outSize])

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
