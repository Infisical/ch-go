package compress

import (
	"bytes"
	"io"
	"testing"

	"github.com/go-faster/city"
	"github.com/stretchr/testify/require"
)

func TestFormatU128(t *testing.T) {
	v := city.CH128([]byte("Moscow"))
	require.Equal(t, "6ddf3eeebf17df2e559d40c605f3ae22", FormatU128(v))
}

// A caller enforcing a memory budget is told what a frame adds to the buffers the reader keeps, so
// that figure must never come in under what those buffers actually grew by.
func TestFrameAccountCoversTheBuffersItReports(t *testing.T) {
	sizes := []int{32 << 10, (32 << 10) + 1, 64 << 10, (64 << 10) + 1}

	var stream bytes.Buffer
	for _, size := range sizes {
		w := NewWriter(LevelZero, LZ4)
		require.NoError(t, w.Compress(bytes.Repeat([]byte("x"), size)))
		stream.Write(w.Data)
	}

	var charged int
	r := NewReader(bytes.NewReader(stream.Bytes()))
	r.SetFrameAccount(func(n int) error {
		charged += n
		return nil
	})

	for i, size := range sizes {
		out := make([]byte, size)
		_, err := io.ReadFull(r, out)
		require.NoError(t, err, "frame %d", i)
		require.Equal(t, bytes.Repeat([]byte("x"), size), out, "frame %d", i)
		require.GreaterOrEqual(t, charged, cap(r.data)+cap(r.raw)-allocationSlack,
			"frame %d reported less than the buffers it grew", i)
	}

	_, err := r.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF, "every frame must have been read")

}

// make rounds a size up to a page, which the reader cannot see before it allocates.
const allocationSlack = 8 << 10
