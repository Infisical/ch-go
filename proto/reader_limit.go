package proto

import "github.com/go-faster/errors"

// ErrReadLimit is returned when a decode would read or allocate past the reader limit.
var ErrReadLimit = errors.New("read limit exceeded")

// SetLimit bounds how many bytes the reader may read or allocate before failing, which makes a
// length taken off the wire safe to act on. Zero, the default, means no limit.
//
// Decoding data from an untrusted peer should set this per packet: the protocol has decoders that
// allocate a declared length before reading it, so an unlimited reader allocates whatever the peer
// claims. The limit also bounds the sizes a single compressed frame may declare.
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

// Take charges n bytes against the limit before they are read or allocated.
func (r *Reader) Take(n int) error {
	if !r.limited {
		return nil
	}
	if n < 0 {
		return errors.Wrapf(ErrReadLimit, "negative size %d", n)
	}
	if n > r.limit {
		return errors.Wrapf(ErrReadLimit, "%d bytes with %d remaining", n, r.limit)
	}
	r.limit -= n
	return nil
}
