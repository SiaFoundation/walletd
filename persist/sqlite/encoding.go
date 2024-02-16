package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"go.sia.tech/core/types"
)

func encode(obj any) any {
	switch obj := obj.(type) {
	case types.Currency:
		// Currency is encoded as two 64-bit big-endian integers for sorting
		buf := make([]byte, 16)
		binary.BigEndian.PutUint64(buf, obj.Hi)
		binary.BigEndian.PutUint64(buf[8:], obj.Lo)
		return buf
	case types.EncoderTo:
		var buf bytes.Buffer
		e := types.NewEncoder(&buf)
		obj.EncodeTo(e)
		e.Flush()
		return buf.Bytes()
	case uint64:
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, obj)
		return b
	case time.Time:
		return obj.Unix()
	default:
		panic(fmt.Sprintf("dbEncode: unsupported type %T", obj))
	}
}

type decodable struct {
	v any
}

// Scan implements the sql.Scanner interface.
func (d *decodable) Scan(src any) error {
	if src == nil {
		return errors.New("cannot scan nil into decodable")
	}

	switch src := src.(type) {
	case []byte:
		switch v := d.v.(type) {
		case *types.Currency:
			if len(src) != 16 {
				return fmt.Errorf("cannot scan %d bytes into Currency", len(src))
			}
			v.Hi = binary.BigEndian.Uint64(src)
			v.Lo = binary.BigEndian.Uint64(src[8:])
		case types.DecoderFrom:
			dec := types.NewBufDecoder(src)
			v.DecodeFrom(dec)
			return dec.Err()
		case *uint64:
			*v = binary.LittleEndian.Uint64(src)
		default:
			return fmt.Errorf("cannot scan %T to %T", src, d.v)
		}
		return nil
	case int64:
		switch v := d.v.(type) {
		case *uint64:
			*v = uint64(src)
		case *time.Time:
			*v = time.Unix(src, 0).UTC()
		default:
			return fmt.Errorf("cannot scan %T to %T", src, d.v)
		}
		return nil
	default:
		return fmt.Errorf("cannot scan %T to %T", src, d.v)
	}
}

func decode(obj any) sql.Scanner {
	return &decodable{obj}
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
