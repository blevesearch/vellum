package vellum

// ReusableState is an opaque, reusable decode buffer for the incremental
// AcceptWithValState / IsMatchWithValState walk.
//
// The stock AcceptWithVal / IsMatchWithVal decode each source state with a nil
// prealloc, so they allocate a fresh internal state on every transition. Callers
// that walk the FST one byte at a time over many overlapping prefixes — e.g.
// building a CJK segmentation DAG, where the walk restarts from every input
// position — pay one allocation per transition. Holding a single ReusableState
// for the whole walk eliminates that allocation (the internal state only
// references the FST data, so it can be reused in place).
//
// A ReusableState is NOT safe for concurrent use; give each goroutine its own.
type ReusableState struct {
	s fstStateV1
}

// NewReusableState returns a fresh, reusable decode buffer.
func NewReusableState() *ReusableState {
	return &ReusableState{}
}

// AcceptWithValState behaves exactly like AcceptWithVal but decodes the source
// state into the caller-owned rs instead of allocating a new internal state.
func (f *FST) AcceptWithValState(addr int, b byte, rs *ReusableState) (int, uint64) {
	s, err := f.decoder.stateAt(addr, &rs.s)
	if err != nil {
		return noneAddr, 0
	}
	_, next, output := s.TransitionFor(b)
	return next, output
}

// IsMatchWithValState behaves exactly like IsMatchWithVal but decodes the state
// into the caller-owned rs instead of allocating a new internal state.
func (f *FST) IsMatchWithValState(addr int, rs *ReusableState) (bool, uint64) {
	s, err := f.decoder.stateAt(addr, &rs.s)
	if err != nil {
		return false, 0
	}
	return s.Final(), s.FinalOutput()
}
