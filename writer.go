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
	"bufio"
	"fmt"
	"io"
	"log"
)

// A writer is a buffered writer used by vellum. It counts how many bytes have
// been written and has some convenience methods used for encoding the data.
type writer struct {
	w       *bufio.Writer
	counter int
}

func newWriter(w io.Writer) *writer {
	return &writer{
		w: bufio.NewWriter(w),
	}
}

func (w *writer) Reset(newWriter io.Writer) {
	w.w.Reset(newWriter)
	w.counter = 0
}

func (w *writer) WriteByte(c byte) error {
	err := w.w.WriteByte(c)
	if err != nil {
		return err
	}
	w.counter++
	return nil
}

func (w *writer) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.counter += n
	return n, err
}

func (w *writer) Flush() error {
	return w.w.Flush()
}

func (w *writer) WritePackedUintIn(v uint64, n int) error {
	for shift := uint(0); shift < uint(n*8); shift += 8 {
		err := w.WriteByte(byte(v >> shift))
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *writer) WriteByteSlice(v []byte, n int) error {
	for shift := 0; shift < n; shift++ {
		if shift >= len(v) {
			// using 0 may not be a good idea, need to figure out the decoder part of this
			err := w.WriteByte(byte(0))
			if err != nil {
				return err
			}
			continue
		}
		err := w.WriteByte(byte(v[shift]))
		if err != nil {
			return err
		}
	}
	log.Printf("writer says %v %v\n", v, n)
	return nil
}

func (w *writer) WritePackedUint(v uint64) error {
	n := packedSize(v)
	return w.WritePackedUintIn(v, n)
}

// each int in the array is varuint encoded, so each number's
// on disk size could vary hence we'd need to maintain an array,
// the ith element corresponds to the size of the ith element in
// in the intSlice.
// The format would look something like this ?
//
//	     1 byte						  sum(sizesArray) bytes
//	<lenArray/sizesArrayPackSize>              <<array>>

func (w *writer) WriteIntSlice(intSlice []uint64, totalPackSize int) error {
	var totalSizeWritten int
	var n int
	// FYI: traverse and write in reverse order
	// for _, val := range intSlice {
	// 	// n = packedIntSize(val)
	// 	// m corresponds to the packed size of the val's pack size.
	// 	// we want to find the max such pack size which would be
	// 	// written as sizesArrayPackSize
	// 	// because each element in the array of varying size,
	// 	// and we want to store each of those sizes in the sizesArray
	// 	// it makes sense to encode them as per a fixed sizesArrayPackSize
	// 	// so that we can retrieve the same while decoding by just slicing
	// 	// the data array.
	// 	// m := packedIntSize(uint64(n))
	// 	// sizesArrayPackSize = max(sizesArrayPackSize, m)
	// }
	maxSize := 0
	for _, val := range intSlice {
		n = packedIntSize(val)
		if n > maxSize {
			maxSize = n
		}
	}

	for i := len(intSlice) - 1; i >= 0; i-- {
		if totalSizeWritten > totalPackSize {
			return fmt.Errorf("write overflow")
		}
		err := w.WritePackedUintIn(intSlice[i], maxSize)
		if err != nil {
			return err
		}
		totalSizeWritten += maxSize
	}

	elePackSize := packedIntSize(uint64(maxSize))
	err := w.WritePackedUintIn(uint64(maxSize), elePackSize)
	if err != nil {
		return err
	}

	lenPackSize := packedIntSize(uint64(len(intSlice)))
	err = w.WritePackedUintIn(uint64(len(intSlice)), lenPackSize)
	if err != nil {
		return err
	}

	x := encodePackSize(lenPackSize, elePackSize)
	err = w.WriteByte(x)
	if err != nil {
		return err
	}

	// block1:
	// n = packedIntSize(uint64(len(intSlice)))
	// err := w.WritePackedUintIn(uint64(len(intSlice)), n)
	// if err != nil {
	// 	return err
	// }

	// write a byte having the len(intSlice) i.e the number
	// of elements in the intSlice and the sizesArrayPackSize.
	// Also, do we write the packsize of len(intSlice) ie block1? or is
	// len(intSlice) enough?
	// x := encodePackSize(len(intSlice), sizesArrayPackSize)
	// err := e.bw.WriteByte(x)
	// if err != nil {
	// 	return 0, err
	// }
	log.Printf("writer says %v %v %v\n", maxSize, len(intSlice), intSlice)
	return nil
}

func (w *writer) WritePackedOutput(v interface{}, n int) error {
	val, ok := v.([]uint64)
	if ok {
		//write int slice
		return w.WriteIntSlice(val, n)
	}
	valInt, _ := v.(uint64)
	return w.WritePackedUintIn(valInt, n)
}

func packedSliceSize(in []uint64) (rv int) {
	maxSize := 0
	for _, val := range in {
		n := packedIntSize(val)
		if n > maxSize {
			maxSize = n
		}
	}
	rv += len(in) * maxSize

	elePackSize := packedIntSize(uint64(maxSize))

	lenPackSize := packedIntSize(uint64(len(in)))

	return rv + elePackSize + lenPackSize + 1
}

func packedIntSize(n uint64) int {
	if n < 1<<8 {
		return 1
	} else if n < 1<<16 {
		return 2
	} else if n < 1<<24 {
		return 3
	} else if n < 1<<32 {
		return 4
	} else if n < 1<<40 {
		return 5
	} else if n < 1<<48 {
		return 6
	} else if n < 1<<56 {
		return 7
	}
	return 8
}

func packedSize(in interface{}) int {
	intSlice, ok := in.([]uint64)
	if ok {
		return packedSliceSize(intSlice)
	}
	n, _ := in.(uint64)
	return packedIntSize(n)
}
