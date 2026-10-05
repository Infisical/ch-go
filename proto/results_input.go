package proto

import "github.com/go-faster/errors"

// Input converts decoded results back into columns that can be encoded. The declared type is
// carried over, since an inferred Decimal(10, 2) column reads back as the lossier Decimal64.
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

// Forwarded, or reusing the input for another insert leaves this column's rows behind.
func (c declaredColumn) Reset() {
	if v, ok := c.ColInput.(Resettable); ok {
		v.Reset()
	}
}

// Forwarded, since the embedded interface hides it and LowCardinality would lose its state prefix.
func (c declaredColumn) EncodeState(b *Buffer) {
	if v, ok := c.ColInput.(StateEncoder); ok {
		v.EncodeState(b)
	}
}

// Forwarded for the same reason, or a column that prepares itself before encoding would skip it.
func (c declaredColumn) Prepare() error {
	if v, ok := c.ColInput.(Preparable); ok {
		return v.Prepare()
	}
	return nil
}
