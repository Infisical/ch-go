package proto

import (
	"bytes"
	"encoding/binary"
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

// The string decoder guesses at the rows still to come and allocates for all of them, so a small
// first row must not let a large row count reserve what the limit could never deliver.
func TestReaderLimitBoundsTheBatchAllocation(t *testing.T) {
	// Low enough that the position slice fits the limit, so only the batch guess can overrun it.
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

// A caller bounding several readers together needs to see each charge, and to be able to refuse it.
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

// A caller may want to see every charge without capping any single one, so the callback does not
// depend on a limit being set.
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

// A frame's buffers are allocated from its header and outlive the packet that brought them, so a
// caller bounding memory has to see them too.
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
