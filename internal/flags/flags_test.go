package flags

import "testing"

const (
	a uint8 = 1 << iota
	b
	c
)

func TestSet(t *testing.T) {
	var s Set[uint8]
	if !s.IsClr(a | b | c) {
		t.Fatal("zero value not empty")
	}
	s.Set(a | c)
	s.Clr(c)
	testCases := []struct {
		name  string
		f     uint8
		isSet bool
		isClr bool
	}{
		{
			name:  "set",
			f:     a,
			isSet: true,
		},
		{
			name:  "clear",
			f:     b,
			isClr: true,
		},
		{
			name:  "cleared",
			f:     c,
			isClr: true,
		},
		{
			name:  "any set",
			f:     a | b,
			isSet: true,
		},
		{
			name:  "all clear",
			f:     b | c,
			isClr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := s.IsSet(tc.f), tc.isSet; got != want {
				t.Errorf("IsSet: got %v, want %v", got, want)
			}
			if got, want := s.IsClr(tc.f), tc.isClr; got != want {
				t.Errorf("IsClr: got %v, want %v", got, want)
			}
		})
	}
}

func TestValue(t *testing.T) {
	var s Set[uint32]
	s.Set(0b1011)
	if got, want := s.Value(0b0110), uint32(0b0010); got != want {
		t.Errorf("got %#b, want %#b", got, want)
	}
}
