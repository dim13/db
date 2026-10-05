package hash

import "testing"

func TestTorek(t *testing.T) {
	testCases := []struct {
		key  string
		want uint32
	}{
		{key: "", want: 0},
		{key: "A", want: 65},
		{key: "AA", want: 2210},
		{key: "AAA", want: 72995},
		{key: "AAAA", want: 2408900},
		{key: "AAAAA", want: 79493765},
		{key: "AAAAAA", want: 2623294310},
		{key: "AAAAAAA", want: 669366375},
		{key: "AAAAAAAA", want: 614253960},
		{key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", want: 3607767976},
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
