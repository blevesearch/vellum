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
	"io"
	"reflect"
)

var defaultBuilderOpts = &BuilderOpts{
	Encoder:           1,
	outType:           storeIntSlice,
	RegistryTableSize: 10000,
	RegistryMRUSize:   2,
}

// A Builder is used to build a new FST.  When possible data is
// streamed out to the underlying Writer as soon as possible.
type Builder struct {
	unfinished *unfinishedNodes
	registry   *registry
	last       []byte
	len        int

	lastAddr int

	encoder encoder
	opts    *BuilderOpts

	builderNodePool *builderNodePool
}

const storeIntSlice = 42
const noneAddr = 1
const emptyAddr = 0

// NewBuilder returns a new Builder which will stream out the
// underlying representation to the provided Writer as the set is built.
func newBuilder(w io.Writer, opts *BuilderOpts) (*Builder, error) {
	if opts == nil {
		opts = defaultBuilderOpts
	}
	builderNodePool := &builderNodePool{}
	rv := &Builder{
		unfinished:      newUnfinishedNodes(builderNodePool),
		registry:        newRegistry(builderNodePool, opts.RegistryTableSize, opts.RegistryMRUSize),
		builderNodePool: builderNodePool,
		opts:            opts,
		lastAddr:        noneAddr,
	}

	var err error
	rv.encoder, err = loadEncoder(opts.Encoder, w)
	if opts.outType&storeIntSlice > 0 {
		rv.encoder.setOutputType(opts.outType)
	}
	if err != nil {
		return nil, err
	}
	rv.unfinished.outType = opts.outType
	err = rv.encoder.start()
	if err != nil {
		return nil, err
	}
	return rv, nil
}

func (b *Builder) Reset(w io.Writer) error {
	b.unfinished.Reset()
	b.registry.Reset()
	b.lastAddr = noneAddr
	b.encoder.reset(w)
	b.last = nil
	b.len = 0

	err := b.encoder.start()
	if err != nil {
		return err
	}
	return nil
}

// Insert the provided value to the set being built.
// NOTE: values must be inserted in lexicographical order.
func (b *Builder) Insert(key []byte, val interface{}) error {
	// ensure items are added in lexicographic order
	if bytes.Compare(key, b.last) < 0 {
		return ErrOutOfOrder
	}
	if len(key) == 0 {
		b.len = 1
		b.unfinished.setRootOutput(val)
		return nil
	}

	prefixLen, out := b.unfinished.findCommonPrefixAndSetOutput(key, val)
	b.len++
	err := b.compileFrom(prefixLen)
	if err != nil {
		return err
	}
	b.copyLastKey(key)
	b.unfinished.addSuffix(key[prefixLen:], out)

	return nil
}

func (b *Builder) copyLastKey(key []byte) {
	if b.last == nil {
		b.last = make([]byte, 0, 64)
	} else {
		b.last = b.last[:0]
	}
	b.last = append(b.last, key...)
}

// Close MUST be called after inserting all values.
func (b *Builder) Close() error {
	err := b.compileFrom(0)
	if err != nil {
		return err
	}
	root := b.unfinished.popRoot()
	rootAddr, err := b.compile(root)
	if err != nil {
		return err
	}
	return b.encoder.finish(b.len, rootAddr)
}

func (b *Builder) compileFrom(iState int) error {
	addr := noneAddr
	for iState+1 < len(b.unfinished.stack) {
		var node *builderNode
		if addr == noneAddr {
			node = b.unfinished.popEmpty()
		} else {
			node = b.unfinished.popFreeze(addr)
		}
		var err error
		node.outType = b.opts.outType
		addr, err = b.compile(node)
		if err != nil {
			return nil
		}
	}
	b.unfinished.topLastFreeze(addr)
	return nil
}

func (b *Builder) isEmptyFinalOutput(s *builderNode) bool {
	switch b.opts.outType {
	case storeIntSlice:
		// or what about nil []uint64?
		val, _ := s.finalOutput.([]uint64)
		return len(val) == 0
	default:
		val, _ := s.finalOutput.(uint64)
		return val == 0
	}

}

func (b *Builder) compile(node *builderNode) (int, error) {
	if node.final && len(node.trans) == 0 &&
		b.isEmptyFinalOutput(node) {
		return 0, nil
	}
	found, addr, entry := b.registry.entry(node)
	if found {
		return addr, nil
	}
	addr, err := b.encoder.encodeState(node, b.lastAddr)
	if err != nil {
		return 0, err
	}

	b.lastAddr = addr
	entry.addr = addr
	return addr, nil
}

type unfinishedNodes struct {
	stack []*builderNodeUnfinished

	// cache allocates a reasonable number of builderNodeUnfinished
	// objects up front and tries to keep reusing them
	// because the main data structure is a stack, we assume the
	// same access pattern, and don't track items separately
	// this means calls get() and pushXYZ() must be paired,
	// as well as calls put() and popXYZ()
	cache           []builderNodeUnfinished
	outType         int
	builderNodePool *builderNodePool
}

func (u *unfinishedNodes) Reset() {
	u.stack = u.stack[:0]
	for i := 0; i < len(u.cache); i++ {
		u.cache[i] = builderNodeUnfinished{}
	}
	u.pushEmpty(false)
}

func newUnfinishedNodes(p *builderNodePool) *unfinishedNodes {
	rv := &unfinishedNodes{
		stack:           make([]*builderNodeUnfinished, 0, 64),
		cache:           make([]builderNodeUnfinished, 64),
		builderNodePool: p,
	}
	rv.pushEmpty(false)
	return rv
}

// get new builderNodeUnfinished, reusing cache if possible
func (u *unfinishedNodes) get() *builderNodeUnfinished {
	if len(u.stack) < len(u.cache) {
		return &u.cache[len(u.stack)]
	}
	// full now allocate a new one
	return &builderNodeUnfinished{}
}

// return builderNodeUnfinished, clearing it for reuse
func (u *unfinishedNodes) put() {
	if len(u.stack) >= len(u.cache) {
		return
		// do nothing, not part of cache
	}
	u.cache[len(u.stack)] = builderNodeUnfinished{}
}

func (u *unfinishedNodes) isEmptyFinalOutput(out interface{}) bool {
	switch u.outType {
	case storeIntSlice:
		val, _ := out.([]uint64)
		return len(val) == 0
	default:
		val, _ := out.(uint64)
		return val == 0
	}

}

func (u *unfinishedNodes) findCommonPrefixAndSetOutput(key []byte,
	out interface{}) (int, interface{}) {
	var i int
	for i < len(key) {
		if i >= len(u.stack) {
			break
		}
		var addPrefix interface{}
		if !u.stack[i].hasLastT {
			break
		}
		if u.stack[i].lastIn == key[i] {
			commonPre := outputPrefix(u.stack[i].lastOut, out)
			addPrefix = outputSub(u.stack[i].lastOut, commonPre)
			out = outputSub(out, commonPre)
			u.stack[i].lastOut = commonPre
			i++
		} else {
			break
		}

		if !u.isEmptyFinalOutput(addPrefix) {
			u.stack[i].addOutputPrefix(addPrefix)
		}
	}

	return i, out
}

func (u *unfinishedNodes) pushEmpty(final bool) {
	next := u.get()
	next.node = u.builderNodePool.Get()
	next.node.final = final
	u.stack = append(u.stack, next)
}

func (u *unfinishedNodes) popRoot() *builderNode {
	l := len(u.stack)
	var unfinished *builderNodeUnfinished
	u.stack, unfinished = u.stack[:l-1], u.stack[l-1]
	rv := unfinished.node
	u.put()
	return rv
}

func (u *unfinishedNodes) popFreeze(addr int) *builderNode {
	l := len(u.stack)
	var unfinished *builderNodeUnfinished
	u.stack, unfinished = u.stack[:l-1], u.stack[l-1]
	unfinished.lastCompiled(addr)
	rv := unfinished.node
	u.put()
	return rv
}

func (u *unfinishedNodes) popEmpty() *builderNode {
	l := len(u.stack)
	var unfinished *builderNodeUnfinished
	u.stack, unfinished = u.stack[:l-1], u.stack[l-1]
	rv := unfinished.node
	u.put()
	return rv
}

func (u *unfinishedNodes) setRootOutput(out interface{}) {
	u.stack[0].node.final = true
	u.stack[0].node.finalOutput = out
}

func (u *unfinishedNodes) topLastFreeze(addr int) {
	last := len(u.stack) - 1
	u.stack[last].lastCompiled(addr)
}

func (u *unfinishedNodes) zeroOutput() interface{} {
	switch u.outType {
	case storeIntSlice:
		return []uint64{}
	default:
		return 0
	}
}

func (u *unfinishedNodes) addSuffix(bs []byte, out interface{}) {
	if len(bs) == 0 {
		return
	}
	last := len(u.stack) - 1
	u.stack[last].hasLastT = true
	u.stack[last].lastIn = bs[0]
	u.stack[last].lastOut = out
	for _, b := range bs[1:] {
		next := u.get()
		next.node = u.builderNodePool.Get()
		next.hasLastT = true
		next.lastIn = b
		next.lastOut = u.zeroOutput()
		u.stack = append(u.stack, next)
	}
	u.pushEmpty(true)
}

type builderNodeUnfinished struct {
	node     *builderNode
	lastOut  interface{}
	lastIn   byte
	hasLastT bool
	outType  int
}

func (f *builderNodeUnfinished) zeroOutput() interface{} {
	switch f.outType {
	case storeIntSlice:
		return []uint64{}
	default:
		return 0
	}
}
func (b *builderNodeUnfinished) lastCompiled(addr int) {
	if b.hasLastT {
		transIn := b.lastIn
		transOut := b.lastOut
		b.hasLastT = false
		b.lastOut = b.zeroOutput()
		b.node.trans = append(b.node.trans, transition{
			in:   transIn,
			out:  transOut,
			addr: addr,
		})
	}
}

func (b *builderNodeUnfinished) addOutputPrefix(prefix interface{}) {
	if b.node.final {
		b.node.finalOutput = outputCat(prefix, b.node.finalOutput)
	}
	for i := range b.node.trans {
		b.node.trans[i].out = outputCat(prefix, b.node.trans[i].out)
	}
	if b.hasLastT {
		b.lastOut = outputCat(prefix, b.lastOut)
	}
}

type builderNode struct {
	finalOutput interface{}
	trans       []transition
	final       bool
	outType     int
	// intrusive linked list
	next *builderNode
}

// reset resets the receiver builderNode to a re-usable state.
func (n *builderNode) reset() {
	n.final = false
	n.finalOutput = 0
	n.trans = n.trans[:0]
	n.next = nil
}

func areOutputsEquiv(l interface{}, r interface{}) bool {
	lv, ok := l.([]uint64)
	if ok {
		rv, _ := r.([]uint64)
		// is this fine or do we do something else?
		return reflect.DeepEqual(lv, rv)
	}

	lIn, _ := l.(uint64)
	rIn, _ := r.(uint64)
	return lIn == rIn
}

func (n *builderNode) equiv(o *builderNode) bool {
	if n.final != o.final {
		return false
	}
	if !areOutputsEquiv(n.finalOutput, o.finalOutput) {
		return false
	}
	if len(n.trans) != len(o.trans) {
		return false
	}
	for i, ntrans := range n.trans {
		otrans := o.trans[i]
		if ntrans.in != otrans.in {
			return false
		}
		if ntrans.addr != otrans.addr {
			return false
		}
		if !areOutputsEquiv(ntrans.out, otrans.out) {
			return false
		}
	}
	return true
}

type transition struct {
	out  interface{}
	addr int
	in   byte
}

func intSlicePrefixOffset(l, r interface{}) int {
	_l, _ := l.([]uint64)
	_r, _ := r.([]uint64)
	prefIdx := 0
	for i := range _l {
		if i >= len(_r) || _l[i] != _r[i] {
			break
		}
		prefIdx++
	}
	return prefIdx
}

func outputPrefix(l, r interface{}) interface{} {
	_lb, ok := l.([]uint64)
	if ok {
		return _lb[:intSlicePrefixOffset(l, r)]
	}
	_l, _ := l.(uint64)
	_r, _ := r.(uint64)

	if _l < _r {
		return _l
	}
	return _r
}

func intSliceSub(l, r interface{}) []uint64 {
	_l, _ := l.([]uint64)
	_r, _ := r.([]uint64)
	return _l[intSlicePrefixOffset(_l, _r):]
}

func outputSub(l, r interface{}) interface{} {
	_, ok := l.([]uint64)
	if ok {
		return intSliceSub(l, r)
	}
	_l, _ := l.(uint64)
	_r, _ := r.(uint64)
	return _l - _r
}

func intSliceCat(l, r interface{}) []uint64 {
	_l, _ := l.([]uint64)
	_r, _ := r.([]uint64)
	var rv []uint64
	rv = append(rv, _l...)
	rv = append(rv, _r...)
	return rv
}

func outputCat(l, r interface{}) interface{} {
	_, ok := l.([]uint64)
	if ok {
		return intSliceCat(l, r)
	}
	_l, _ := l.(uint64)
	_r, _ := r.(uint64)
	return _l + _r
}

// builderNodePool pools builderNodes using a singly linked list.
//
// NB: builderNode lifecylce is described by the following interactions -
// +------------------------+                            +----------------------+
// |    Unfinished Nodes    |      Transfer once         |        Registry      |
// |(not frozen builderNode)|-----builderNode is ------->| (frozen builderNode) |
// +------------------------+      marked frozen         +----------------------+
//
//	^                                                     |
//	|                                                     |
//	|                                                   Put()
//	| Get() on        +-------------------+             when
//	+-new char--------| builderNode Pool  |<-----------evicted
//	                  +-------------------+
type builderNodePool struct {
	head *builderNode
}

func (p *builderNodePool) Get() *builderNode {
	if p.head == nil {
		return &builderNode{}
	}
	head := p.head
	p.head = p.head.next
	return head
}

func (p *builderNodePool) Put(v *builderNode) {
	if v == nil {
		return
	}
	v.reset()
	v.next = p.head
	p.head = v
}
