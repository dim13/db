package hash

import "testing"

func TestDefaultHash(t *testing.T) {
	testCases := []struct {
		key  string
		want uint32
	}{
		{"", 0},
		{"A", 65},
		{"AA", 2210},
		{"AAA", 72995},
		{"AAAA", 2408900},
		{"AAAAA", 79493765},
		{"AAAAAA", 2623294310},
		{"AAAAAAA", 669366375},
		{"AAAAAAAA", 614253960},
		{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 3607767976},
	}
	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			got := defaultHash([]byte(tc.key))
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func BenchmarkDefaultHash(b *testing.B) {
	benchCases := []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG",
	}
	for _, bc := range benchCases {
		b.Run(bc, func(b *testing.B) {
			key := []byte(bc)
			for i := 0; i < b.N; i++ {
				defaultHash(key)
			}
		})
	}
}
