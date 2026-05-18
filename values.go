package litelog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	errShortBuf   = errors.New("litelog: buffer too short")
	errBadType    = errors.New("litelog: unsupported Go type for inferValueType")
	errTypeMismatch = errors.New("litelog: value does not match declared type")
)

// encodeInt64 encodes n as 8 big-endian bytes.
func encodeInt64(n int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	return b
}

// decodeInt64 decodes 8 big-endian bytes to int64.
func decodeInt64(b []byte) int64 {
	return int64(binary.BigEndian.Uint64(b))
}

// encodeFloat64 produces an order-preserving 8-byte encoding of f.
// Lexicographic comparison of the output matches float64 numeric ordering.
func encodeFloat64(f float64) []byte {
	bits := math.Float64bits(f)
	// Flip sign bit so positives sort after negatives.
	// For negative floats, flip all bits so that more-negative sorts first.
	if bits&(1<<63) != 0 {
		bits = ^bits // negative: flip all bits
	} else {
		bits ^= 1 << 63 // positive: flip sign bit only
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, bits)
	return b
}

// decodeFloat64 reverses encodeFloat64.
func decodeFloat64(b []byte) float64 {
	bits := binary.BigEndian.Uint64(b)
	// Reverse the encoding: check the (now-flipped) sign bit.
	if bits&(1<<63) != 0 {
		bits ^= 1 << 63 // was positive: flip sign bit back
	} else {
		bits = ^bits // was negative: flip all bits back
	}
	return math.Float64frombits(bits)
}

// encodeValue converts a Go value to bytes for SQLite BLOB storage.
func encodeValue(v any, vtype ValueType) ([]byte, error) {
	switch vtype {
	case TypeInt:
		switch n := v.(type) {
		case int64:
			return encodeInt64(n), nil
		case int:
			return encodeInt64(int64(n)), nil
		default:
			return nil, fmt.Errorf("%w: want int/int64 for TypeInt, got %T", errTypeMismatch, v)
		}

	case TypeRef:
		switch n := v.(type) {
		case int64:
			return encodeInt64(n), nil
		case int:
			return encodeInt64(int64(n)), nil
		default:
			return nil, fmt.Errorf("%w: want int/int64 for TypeRef, got %T", errTypeMismatch, v)
		}

	case TypeString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%w: want string for TypeString, got %T", errTypeMismatch, v)
		}
		return []byte(s), nil

	case TypeFloat:
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("%w: want float64 for TypeFloat, got %T", errTypeMismatch, v)
		}
		return encodeFloat64(f), nil

	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: want bool for TypeBool, got %T", errTypeMismatch, v)
		}
		if b {
			return []byte{0x01}, nil
		}
		return []byte{0x00}, nil

	case TypeBytes:
		bs, ok := v.([]byte)
		if !ok {
			return nil, fmt.Errorf("%w: want []byte for TypeBytes, got %T", errTypeMismatch, v)
		}
		return bs, nil

	case TypeInst:
		t, ok := v.(time.Time)
		if !ok {
			return nil, fmt.Errorf("%w: want time.Time for TypeInst, got %T", errTypeMismatch, v)
		}
		ms := t.UnixMilli()
		return encodeInt64(ms), nil

	default:
		return nil, fmt.Errorf("litelog: unknown ValueType %d", vtype)
	}
}

// decodeValue converts bytes from SQLite back to a Go value.
func decodeValue(b []byte, vtype ValueType) (any, error) {
	switch vtype {
	case TypeInt:
		if len(b) < 8 {
			return nil, errShortBuf
		}
		return decodeInt64(b), nil

	case TypeRef:
		if len(b) < 8 {
			return nil, errShortBuf
		}
		return decodeInt64(b), nil

	case TypeString:
		return string(b), nil

	case TypeFloat:
		if len(b) < 8 {
			return nil, errShortBuf
		}
		return decodeFloat64(b), nil

	case TypeBool:
		if len(b) < 1 {
			return nil, errShortBuf
		}
		return b[0] != 0x00, nil

	case TypeBytes:
		return b, nil

	case TypeInst:
		if len(b) < 8 {
			return nil, errShortBuf
		}
		ms := decodeInt64(b)
		return time.UnixMilli(ms), nil

	default:
		return nil, fmt.Errorf("litelog: unknown ValueType %d", vtype)
	}
}

// inferValueType returns the ValueType for a Go value.
func inferValueType(v any) (ValueType, error) {
	switch v.(type) {
	case int64, int:
		return TypeInt, nil
	case string:
		return TypeString, nil
	case float64:
		return TypeFloat, nil
	case bool:
		return TypeBool, nil
	case []byte:
		return TypeBytes, nil
	case time.Time:
		return TypeInst, nil
	default:
		return 0, fmt.Errorf("%w: %T", errBadType, v)
	}
}
