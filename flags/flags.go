// Package flags implements a set of bit flags.
package flags

// Set is a set of bit flags; the zero value is empty.
type Set[T ~uint8 | ~uint32] struct {
	v T
}

// Set sets f.
func (s *Set[T]) Set(f T) {
	s.v |= f
}

// IsSet reports whether any of f is set.
func (s Set[T]) IsSet(f T) bool {
	return s.v&f != 0
}

// IsClr reports whether all of f are clear.
func (s Set[T]) IsClr(f T) bool {
	return s.v&f == 0
}

// Clr clears f.
func (s *Set[T]) Clr(f T) {
	s.v &^= f
}

// Value returns the flags selected by mask.
func (s Set[T]) Value(mask T) T {
	return s.v & mask
}
