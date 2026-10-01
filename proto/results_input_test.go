package proto

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Column types Infer does not cover, listed so the test asserts they are refused rather than
// silently mis-parsed.
var uninferableBlocks = map[string]bool{
	"fixedstring.native":        true,
	"lc_nullable_string.native": true,
}

func decodeBlockFully(payload []byte) (Block, Results, int, error) {
	var (
		block   Block
		decoded Results
	)
	counting := &byteCountingReader{src: payload}
	r := NewReader(counting)
	r.SetLimit(1 << 20)
	// A FORMAT Native dump carries no per-column serialization flag, so it reads at revision zero.
	err := block.DecodeRawBlock(r, 0, decoded.Auto())
	return block, decoded, counting.read, err
}

// One byte at a time, so the buffered reader cannot count bytes the decoder never asked for.
type byteCountingReader struct {
	src  []byte
	read int
}

func (c *byteCountingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.read >= len(c.src) {
		return 0, io.EOF
	}
	p[0] = c.src[c.read]
	c.read++
	return 1, nil
}

// Blocks captured from a real ClickHouse with SELECT ... FORMAT Native, so a decode and re-encode
// is checked against the server's own bytes rather than against this package agreeing with itself.
func TestResultsInputRoundTripsClickHousesOwnBlocks(t *testing.T) {
	files, err := filepath.Glob("testdata/clickhouse_blocks/*.native")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			payload, err := os.ReadFile(file)
			require.NoError(t, err)

			block, decoded, read, decodeErr := decodeBlockFully(payload)
			if uninferableBlocks[filepath.Base(file)] {
				require.Error(t, decodeErr)
				return
			}
			require.NoError(t, decodeErr)
			require.Equal(t, len(payload), read, "the decode must land on the block's last byte")

			input, err := decoded.Input()
			require.NoError(t, err)
			for i, column := range input {
				require.Equal(t, decoded[i].Type, column.Data.Type(), "column %q lost its type", column.Name)
			}

			// Re-encoding must be stable, or a block would change every time it passed through.
			var once Buffer
			require.NoError(t, block.EncodeRawBlock(&once, 0, input))
			block, decoded, read, err = decodeBlockFully(once.Buf)
			require.NoError(t, err)
			require.Equal(t, len(once.Buf), read)
			again, err := decoded.Input()
			require.NoError(t, err)
			var twice Buffer
			require.NoError(t, block.EncodeRawBlock(&twice, 0, again))
			require.True(t, bytes.Equal(once.Buf, twice.Buf))
		})
	}
}

func decimalBlock(t *testing.T, declared string, values ColDecimal64) []byte {
	t.Helper()

	var b Buffer
	require.NoError(t, Block{Columns: 1, Rows: len(values)}.EncodeRawBlock(&b, 0, Input{
		{Name: "d", Data: declaredColumn{ColInput: &values, declared: ColumnType(declared)}},
	}))
	return b.Buf
}

// A caller that decodes into its own typed columns gets no inference, so the declared type has to
// be recorded there too or a re-encode writes Decimal64 and drops the precision.
func TestResultsInputKeepsADeclaredTypeOnTypedResults(t *testing.T) {
	payload := decimalBlock(t, "Decimal(10, 2)", ColDecimal64{100, 250})

	var (
		block   Block
		target  ColDecimal64
		results = Results{{Name: "d", Data: &target}}
	)
	r := NewReader(bytes.NewReader(payload))
	r.SetLimit(1 << 20)
	require.NoError(t, block.DecodeRawBlock(r, 0, results))

	input, err := results.Input()
	require.NoError(t, err)
	require.Equal(t, ColumnType("Decimal(10, 2)"), input[0].Data.Type())

	var again Buffer
	require.NoError(t, block.EncodeRawBlock(&again, 0, input))
	require.Equal(t, payload, again.Buf)
}

// Reusing the converted input for another insert must clear every column, wrapped or not.
func TestResultsInputResetsAWrappedColumn(t *testing.T) {
	var values ColDecimal64
	values.Append(7)
	input := Input{{Name: "d", Data: declaredColumn{ColInput: &values, declared: "Decimal(10, 2)"}}}

	input.Reset()
	require.Zero(t, input[0].Data.Rows(), "a wrapped column must be cleared with the rest")
}
