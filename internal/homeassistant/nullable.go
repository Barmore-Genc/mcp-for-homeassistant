package homeassistant

import "encoding/json"

// Nullable is a field of a partial update that distinguishes "leave unchanged"
// (the zero value), "clear" (Null) and "set" (Set). Use it with `omitzero`.
type Nullable[T any] struct {
	value T
	set   bool
	null  bool
}

// Set returns a Nullable that sets the field to v.
func Set[T any](v T) Nullable[T] { return Nullable[T]{value: v, set: true} }

// Null returns a Nullable that clears the field.
func Null[T any]() Nullable[T] { return Nullable[T]{set: true, null: true} }

// IsZero reports whether the field is left unchanged.
func (n Nullable[T]) IsZero() bool { return !n.set }

// Get returns the value and whether it is set to a non-null value.
func (n Nullable[T]) Get() (T, bool) { return n.value, n.set && !n.null }

// IsNull reports whether the field is explicitly cleared.
func (n Nullable[T]) IsNull() bool { return n.set && n.null }

func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if !n.set || n.null {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}

func (n *Nullable[T]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = Null[T]()
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*n = Set(v)
	return nil
}
