package hash

import "testing"

func TestTorek(t *testing.T) {
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
			h := newTorek()
			h.Write([]byte(tc.key))
			got := h.Sum32()
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func BenchmarkTorek(b *testing.B) {
	benchCases := []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG",
	}
	for _, bc := range benchCases {
		b.Run(bc, func(b *testing.B) {
			key := []byte(bc)
			h := newTorek()
			for b.Loop() {
				h.Reset()
				h.Write(key)
				h.Sum32()
			}
		})
	}
}
