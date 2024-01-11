package sqlite

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"time"

	"go.sia.tech/core/types"
)

type (
	sqlCurrency types.Currency
	sqlTime     time.Time
)

// Scan implements the sql.Scanner interface.
func (sc *sqlCurrency) Scan(src any) error {
	buf, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("cannot scan %T to Currency", src)
	} else if len(buf) != 16 {
		return fmt.Errorf("cannot scan %d bytes to Currency", len(buf))
	}

	sc.Lo = binary.LittleEndian.Uint64(buf[:8])
	sc.Hi = binary.LittleEndian.Uint64(buf[8:])
	return nil
}

// Value implements the driver.Valuer interface.
func (sc sqlCurrency) Value() (driver.Value, error) {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[:8], sc.Lo)
	binary.LittleEndian.PutUint64(buf[8:], sc.Hi)
	return buf, nil
}

func (st *sqlTime) Scan(src any) error {
	switch src := src.(type) {
	case int64:
		*st = sqlTime(time.Unix(src, 0))
		return nil
	default:
		return fmt.Errorf("cannot scan %T to Time", src)
	}
}

func (st sqlTime) Value() (driver.Value, error) {
	return time.Time(st).Unix(), nil
}

func encode[T types.EncoderTo](v T) []byte {
	var buf bytes.Buffer
	enc := types.NewEncoder(&buf)
	v.EncodeTo(enc)
	if err := enc.Flush(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func encodeSlice[T types.EncoderTo](v []T) []byte {
	var buf bytes.Buffer
	enc := types.NewEncoder(&buf)
	enc.WritePrefix(len(v))
	for _, e := range v {
		e.EncodeTo(enc)
	}
	if err := enc.Flush(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type decodableSlice[T any] struct {
	v *[]T
}

func (d *decodableSlice[T]) Scan(src any) error {
	switch src := src.(type) {
	case []byte:
		dec := types.NewBufDecoder(src)
		s := make([]T, dec.ReadPrefix())
		for i := range s {
			dv, ok := any(&s[i]).(types.DecoderFrom)
			if !ok {
				panic(fmt.Errorf("cannot decode %T", s[i]))
			}
			dv.DecodeFrom(dec)
		}
		if err := dec.Err(); err != nil {
			return err
		}
		*d.v = s
		return nil
	default:
		return fmt.Errorf("cannot scan %T to []byte", src)
	}
}

func decodeSlice[T any](v *[]T) sql.Scanner {
	return &decodableSlice[T]{v: v}
}

type decodable[T types.DecoderFrom] struct {
	v T
}

// Scan implements the sql.Scanner interface.
func (d *decodable[T]) Scan(src any) error {
	switch src := src.(type) {
	case []byte:
		dec := types.NewBufDecoder(src)
		d.v.DecodeFrom(dec)
		return dec.Err()
	default:
		return fmt.Errorf("cannot scan %T to []byte", src)
	}
}

func decode[T types.DecoderFrom](v T) sql.Scanner {
	return &decodable[T]{v}
}
