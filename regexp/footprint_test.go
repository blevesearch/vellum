package regexp

import (
	"testing"
)

// BenchmarkDFAFootprint tracks the heap footprint of compiling regexps of
// varying complexity. The transition tables (state.next) dominate the
// allocation, so this guards against regressions in their size.
func BenchmarkDFAFootprint(b *testing.B) {
	exprs := []string{"my.*h", "[a-z]+@[a-z]+\\.(com|net|org)", "(abc|def|ghi)*[0-9]{2,4}foo.*bar"}
	for _, e := range exprs {
		b.Run(e, func(b *testing.B) {
			b.ReportAllocs()
			var states int
			for i := 0; i < b.N; i++ {
				r, err := New(e)
				if err != nil {
					b.Fatal(err)
				}
				states = len(r.dfa.states)
			}
			b.ReportMetric(float64(states), "states")
		})
	}
}
