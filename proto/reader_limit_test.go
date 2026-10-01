package proto

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
)

func limitedReaderOver(t *testing.T, payload []byte, limit int) *Reader {
	t.Helper()
	r := NewReader(bytes.NewReader(payload))
	r.SetLimit(limit)
	return r
}

func blockHeader(columnType string, rows uint64) *Buffer {
	b := new(Buffer)
	b.PutUVarInt(1)
	b.PutUVarInt(rows)
	b.PutString("c")
	b.PutString(columnType)
	b.PutBool(false)
	return b
}

// A size read off the wire is allocated before it is read, so without a limit a peer can make the
// decoder allocate whatever it claims. These are the sizes a peer controls.
func TestReaderLimitRefusesASizeBeforeAllocatingIt(t *testing.T) {
	for name, build := range map[string]func() []byte{
		"a string row": func() []byte {
			b := blockHeader("String", 1)
			b.PutUVarInt(4 << 30)
			return append(b.Buf, []byte("tiny")...)
		},
		"an array element count": func() []byte {
			b := blockHeader("Array(UInt64)", 1)
			b.PutUInt64(100_000_000)
			return append(b.Buf, []byte("tiny")...)
		},
		"a row count": func() []byte {
			return append(blockHeader("UUID", 100_000_000).Buf, []byte("tiny")...)
		},
		"a fixed string width": func() []byte {
			return append(blockHeader("FixedString(16)", 50_000_000).Buf, []byte("tiny")...)
		},
		"an array of uuid": func() []byte {
			b := blockHeader("Array(UUID)", 1)
			b.PutUInt64(100_000_000)
			return append(b.Buf, []byte("tiny")...)
		},
		"an array of string": func() []byte {
			b := blockHeader("Array(String)", 1)
			b.PutUInt64(100_000_000)
			return append(b.Buf, []byte("tiny")...)
		},
		"an array of bool": func() []byte {
			b := blockHeader("Array(Bool)", 1)
			b.PutUInt64(100_000_000)
			return append(b.Buf, []byte("tiny")...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				block   Block
				decoded Results
			)
			err := block.DecodeRawBlock(limitedReaderOver(t, build(), 1<<20), Version, decoded.Auto())
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrReadLimit), "got %v", err)
		})
	}
}

// The point of refusing early is that the claim costs what arrived, not what it asked for.
func TestReaderLimitDoesNotAllocateWhatItRefuses(t *testing.T) {
	b := blockHeader("String", 1)
	b.PutUVarInt(512 << 20)
	payload := append(b.Buf, make([]byte, 64)...)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var (
		block   Block
		decoded Results
	)
	err := block.DecodeRawBlock(limitedReaderOver(t, payload, 1<<20), Version, decoded.Auto())
	runtime.ReadMemStats(&after)

	require.Error(t, err)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4<<20))
}

// A retained buffer can hold the next string without allocating, and the bytes still have to be
// charged or a reused column reads past the limit.
func TestReaderLimitChargesAReusedBuffer(t *testing.T) {
	var wide ColStr
	wide.Append(string(make([]byte, 4096)))
	var encoded Buffer
	wide.EncodeColumn(&encoded)

	var col ColStr
	require.NoError(t, col.DecodeColumn(limitedReaderOver(t, encoded.Buf, 1<<20), 1))

	col.Reset()
	r := limitedReaderOver(t, encoded.Buf, 64)
	require.True(t, errors.Is(col.DecodeColumn(r, 1), ErrReadLimit))
}

func TestReaderLimitLeavesAnUnlimitedReaderAlone(t *testing.T) {
	var strs ColStr
	strs.AppendArr([]string{"a", "bb"})
	var b Buffer
	strs.EncodeColumn(&b)

	for name, limit := range map[string]int{"unlimited": 0, "generous": 1 << 20} {
		t.Run(name, func(t *testing.T) {
			r := NewReader(bytes.NewReader(b.Buf))
			r.SetLimit(limit)
			var got ColStr
			require.NoError(t, got.DecodeColumn(r, 2))
			require.Equal(t, []string{"a", "bb"}, []string{got.Row(0), got.Row(1)})
		})
	}
}

func TestReaderLimitBoundsACompressedFrame(t *testing.T) {
	frame := make([]byte, 25)
	frame[16] = 0x82
	frame[17], frame[18], frame[19], frame[20] = 0x00, 0x00, 0x40, 0x06
	frame[21], frame[22], frame[23], frame[24] = 0x00, 0x00, 0x40, 0x06

	r := limitedReaderOver(t, frame, 1<<20)
	r.EnableCompression()
	_, err := r.UVarInt()
	require.ErrorContains(t, err, "size should be")
}
