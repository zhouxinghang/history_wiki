package httpapi

import "testing"

func TestProminenceForSpanBoundaries(t *testing.T) {
	tests := []struct {
		span float64
		want int
	}{
		{span: 1199.999999, want: 3},
		{span: 1200, want: 2},
		{span: 3999.999999, want: 2},
		{span: 4000, want: 1},
	}
	for _, test := range tests {
		if got := prominenceForSpan(test.span); got != test.want {
			t.Fatalf("prominenceForSpan(%v) = %d, want %d", test.span, got, test.want)
		}
	}
}
