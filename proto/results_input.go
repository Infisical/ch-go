package proto

import "github.com/go-faster/errors"

// Input converts decoded results back into columns that can be encoded, which is what a proxy or a
// round trip needs. The declared type is carried over, because an inferred column reports a lossier
// one: a Decimal(10, 2) column reads back as Decimal64 and would otherwise be re-encoded as that.
func (s Results) Input() (Input, error) {
	input := make(Input, 0, len(s))
	for _, column := range s {
		data, ok := column.Data.(ColInput)
		if !ok {
			return nil, errors.Errorf("column %q cannot be encoded", column.Name)
		}
		if column.Type != "" && column.Type != data.Type() {
			data = declaredColumn{ColInput: data, declared: column.Type}
		}
		input = append(input, InputColumn{Name: column.Name, Data: data})
	}
	return input, nil
}

type declaredColumn struct {
	ColInput
	declared ColumnType
}

func (c declaredColumn) Type() ColumnType { return c.declared }

// Forwarded, or a column that carries either, such as LowCardinality, is encoded without it.

func (c declaredColumn) EncodeState(b *Buffer) {
	if v, ok := c.ColInput.(StateEncoder); ok {
		v.EncodeState(b)
	}
}

func (c declaredColumn) Prepare() error {
	if v, ok := c.ColInput.(Preparable); ok {
		return v.Prepare()
	}
	return nil
}
