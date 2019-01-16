package hash

import (
	"fmt"
	"testing"
)

func TestLog2(t *testing.T) {
	testCases := []struct {
		num, want uint32
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{3, 2},
		{4, 2},
		{8, 3},
		{9, 4},
		{128, 7},
		{129, 8},
		{1024, 10},
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
			for i := 0; i < b.N; i++ {
				log2(1024)
			}
		})
	}
}
