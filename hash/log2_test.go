package hash

import (
	"fmt"
	"testing"
)

func TestLog2(t *testing.T) {
	testCases := []struct {
		num, want uint32
	}{
		{num: 0, want: 0},
		{num: 1, want: 0},
		{num: 2, want: 1},
		{num: 3, want: 2},
		{num: 4, want: 2},
		{num: 7, want: 3},
		{num: 8, want: 3},
		{num: 15, want: 4},
		{num: 16, want: 4},
		{num: 31, want: 5},
		{num: 32, want: 5},
		{num: 63, want: 6},
		{num: 64, want: 6},
		{num: 127, want: 7},
		{num: 128, want: 7},
		{num: 255, want: 8},
		{num: 256, want: 8},
		{num: 511, want: 9},
		{num: 512, want: 9},
		{num: 1023, want: 10},
		{num: 1024, want: 10},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("log2(%v)=%v", tc.num, tc.want), func(t *testing.T) {
			got := log2(tc.num)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func BenchmarkLog2(b *testing.B) {
	benchCases := []uint32{1, 1024}
	for _, bc := range benchCases {
		b.Run(fmt.Sprintf("log(%v)", bc), func(b *testing.B) {
			for b.Loop() {
				log2(bc)
			}
		})
	}
}
