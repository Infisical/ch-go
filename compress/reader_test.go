package compress

import (
	"bytes"
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
	var stream bytes.Buffer
	for _, size := range []int{32 << 10, (32 << 10) + 1, 64 << 10, (64 << 10) + 1} {
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

	out := make([]byte, 64)
	for {
		if _, err := r.Read(out); err != nil {
			break
		}
		require.GreaterOrEqual(t, charged, cap(r.data)+cap(r.raw)-allocationSlack,
			"a frame reported less than the buffers it grew")
	}
	require.Positive(t, charged)
}

// make rounds a size up to a page, which the reader cannot see before it allocates.
const allocationSlack = 8 << 10
