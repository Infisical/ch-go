package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
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

func TestReaderLimitBoundsTheBatchAllocation(t *testing.T) {
	const rows = 2 << 20
	b := blockHeader("String", rows)
	for i := 0; i < 8; i++ {
		b.PutUVarInt(127)
		b.Buf = append(b.Buf, make([]byte, 127)...)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var (
		block   Block
		decoded Results
	)
	err := block.DecodeRawBlock(limitedReaderOver(t, b.Buf, 64<<20), Version, decoded.Auto())
	runtime.ReadMemStats(&after)

	require.Error(t, err)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(96<<20),
		"a 127 byte row must not reserve 127 bytes for every declared row")
}

func TestReaderLimitReportsEachChargeToTheCaller(t *testing.T) {
	var strs ColStr
	strs.AppendArr([]string{"a", "bb"})
	var encoded Buffer
	strs.EncodeColumn(&encoded)

	t.Run("charges are reported", func(t *testing.T) {
		var charged int
		r := limitedReaderOver(t, encoded.Buf, 1<<20)
		r.SetOnTake(func(n int) error {
			charged += n
			return nil
		})
		var got ColStr
		require.NoError(t, got.DecodeColumn(r, 2))
		require.Positive(t, charged)
	})

	t.Run("a refused charge stops the decode", func(t *testing.T) {
		stop := errors.New("no room")
		r := limitedReaderOver(t, encoded.Buf, 1<<20)
		r.SetOnTake(func(int) error { return stop })
		var got ColStr
		require.ErrorIs(t, got.DecodeColumn(r, 2), stop)
	})
}

func TestReaderLimitReportsChargesWithoutALimit(t *testing.T) {
	var strs ColStr
	strs.AppendArr([]string{"a", "bb"})
	var encoded Buffer
	strs.EncodeColumn(&encoded)

	stop := errors.New("no room")
	r := NewReader(bytes.NewReader(encoded.Buf))
	r.SetOnTake(func(int) error { return stop })
	var got ColStr
	require.ErrorIs(t, got.DecodeColumn(r, 2), stop)
}

func TestReaderLimitReportsAFrameToTheCaller(t *testing.T) {
	frame := func(rawSize, dataSize uint32) []byte {
		b := make([]byte, 25)
		b[16] = 0x82
		binary.LittleEndian.PutUint32(b[17:], rawSize+9)
		binary.LittleEndian.PutUint32(b[21:], dataSize)
		return b
	}

	t.Run("the frame is charged", func(t *testing.T) {
		var charged int
		r := NewReader(bytes.NewReader(frame(64, 128)))
		r.EnableCompression()
		r.SetFrameAccount(func(n int) error {
			charged += n
			return nil
		})
		_, err := r.UVarInt()
		require.Error(t, err, "the frame carries no real payload")
		require.Equal(t, 128+64+25, charged)
	})

	t.Run("a refused frame stops the read", func(t *testing.T) {
		stop := errors.New("no room")
		r := NewReader(bytes.NewReader(frame(64, 128)))
		r.EnableCompression()
		r.SetFrameAccount(func(int) error { return stop })
		_, err := r.UVarInt()
		require.ErrorIs(t, err, stop)
	})
}

// Inference recurses once per wrapper, and a type is as deep as the peer says, so a deep enough one
// overflows the stack. That is fatal in Go and no recover reaches it, so it has to be refused first.
func TestReaderLimitRefusesATypeTooDeepToInfer(t *testing.T) {
	nest := func(depth int) ColumnType {
		return ColumnType(strings.Repeat("Array(", depth) + "UInt8" + strings.Repeat(")", depth))
	}

	t.Run("a type deeper than the limit", func(t *testing.T) {
		var c ColAuto
		require.ErrorContains(t, c.Infer(nest(MaxTypeNesting+1)), "more than the 128 supported")
	})

	t.Run("a type any real schema would use", func(t *testing.T) {
		var c ColAuto
		require.NoError(t, c.Infer("Array(String)"))
	})

	// An enum label is text, so its brackets are not wrappers and must not count as nesting.
	t.Run("an enum whose labels are full of brackets", func(t *testing.T) {
		labels := make([]string, 200)
		for i := range labels {
			labels[i] = fmt.Sprintf("'Error (code %d)' = %d", i, i+1)
		}
		var c ColAuto
		require.NoError(t, c.Infer(ColumnType("Enum16("+strings.Join(labels, ", ")+")")))
	})

	// The gateway reaches inference through a block, so that is where the refusal has to land.
	t.Run("through a decoded block", func(t *testing.T) {
		b := new(Buffer)
		b.PutUVarInt(1)
		b.PutUVarInt(0)
		b.PutString("c")
		b.PutString(string(nest(1 << 20)))
		b.PutBool(false)

		var (
			block   Block
			decoded Results
		)
		err := block.DecodeRawBlock(limitedReaderOver(t, b.Buf, 64<<20), Version, decoded.Auto())
		require.ErrorContains(t, err, "more than the 128 supported")
	})
}
