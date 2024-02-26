package postgres

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
	"go.sia.tech/core/types"
)

func encode(obj any) any {
	switch obj := obj.(type) {
	case time.Duration:
		return obj.Milliseconds()
	case time.Time:
		panic("time.Time is directly supported by Postgres")
	case json.RawMessage:
		return string(obj)
	case types.Currency:
		return obj.ExactString()
	case types.EncoderTo:
		var buf bytes.Buffer
		enc := types.NewEncoder(&buf)
		obj.EncodeTo(enc)
		if err := enc.Flush(); err != nil {
			panic(err)
		}
		return buf.Bytes()
	case uint64:
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, obj)
		return b
	case []types.Hash256:
		v := make([]string, len(obj))
		for i, h := range obj {
			v[i] = hex.EncodeToString(h[:])
		}
		return pq.Array(v)
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
	case string:
		switch v := d.v.(type) {
		case *json.RawMessage:
			*v = json.RawMessage(src)
		default:
			return fmt.Errorf("cannot scan %T to %T", src, d.v)
		}
		return nil
	case []byte:
		switch v := d.v.(type) {
		case *json.RawMessage:
			*v = json.RawMessage(src)
		case *[]types.Hash256:
			var stringArr pq.StringArray

			if err := stringArr.Scan(src); err != nil {
				return fmt.Errorf("failed to scan string array: %w", err)
			}

			*v = make([]types.Hash256, len(stringArr))
			for i, s := range stringArr {
				if err := (*v)[i].UnmarshalText([]byte(s)); err != nil {
					return fmt.Errorf("failed to scan %q into Hash256: %w", s, err)
				}
			}
		case *types.Currency:
			value, err := types.ParseCurrency(string(src))
			if err != nil {
				return fmt.Errorf("failed to scan %q into Currency: %w", src, err)
			}
			*v = value
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
		case *time.Duration:
			*v = time.Duration(src) * time.Millisecond
		case *uint64:
			*v = uint64(src)
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
