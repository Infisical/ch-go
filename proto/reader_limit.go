package proto

import "github.com/go-faster/errors"

// ErrReadLimit is returned when a decode would read or allocate past the reader limit.
var ErrReadLimit = errors.New("read limit exceeded")

// SetLimit bounds how many bytes the reader may read or allocate before failing, and the sizes a
// compressed frame may declare. Zero, the default, means no limit.
func (r *Reader) SetLimit(n int) {
	r.limit = n
	r.limited = n > 0
	if r.frameLimitedTo != nil {
		r.frameLimitedTo.SetFrameLimit(n)
	}
}

// Limit reports the bytes left before the limit is reached, or zero if there is no limit.
func (r *Reader) Limit() int {
	if !r.limited {
		return 0
	}
	return r.limit
}

// SetOnTake reports each charge before it is allowed, so a caller can account the bytes against a
// budget of its own, or refuse them. It is called whether or not a limit is set. Nil clears it.
func (r *Reader) SetOnTake(f func(n int) error) { r.onTake = f }

// SetFrameAccount reports what a compressed frame adds to the buffers the reader keeps, which
// later frames reuse, so the budget charged has to outlive a single packet. Nil clears it.
func (r *Reader) SetFrameAccount(f func(n int) error) {
	if r.frameLimitedTo != nil {
		r.frameLimitedTo.SetFrameAccount(f)
	}
}

// Take charges n bytes against the limit before they are read or allocated.
func (r *Reader) Take(n int) error {
	if !r.limited && r.onTake == nil {
		return nil
	}
	if n < 0 {
		return errors.Wrapf(ErrReadLimit, "negative size %d", n)
	}
	if r.limited && n > r.limit {
		return errors.Wrapf(ErrReadLimit, "%d bytes with %d remaining", n, r.limit)
	}
	if r.onTake != nil {
		if err := r.onTake(n); err != nil {
			return err
		}
	}
	if r.limited {
		r.limit -= n
	}
	return nil
}
