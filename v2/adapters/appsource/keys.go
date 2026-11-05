package appsource

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"time"
)

// Assignment follows Python dictionary equality: bool/int/float keys can coalesce, but strings never
// coalesce with numbers. The first key's type and position remain when its value is overwritten.
func equalKey(a, b any) bool {
	x, y := keyNumber(a), keyNumber(b)
	if x != nil && y != nil {
		return x.Cmp(y) == 0
	}
	switch a := a.(type) {
	case []byte:
		b, ok := b.([]byte)
		return ok && bytes.Equal(a, b)
	case time.Time:
		b, ok := b.(time.Time)
		return ok && a.Equal(b)
	case float64:
		b, ok := b.(float64)
		return ok && sameFloatKey(a, b)
	default:
		return a == b
	}
}
func keyNumber(v any) *big.Rat {
	switch v := v.(type) {
	case bool:
		if v {
			return new(big.Rat).SetInt64(1)
		}
		return new(big.Rat)
	case int64:
		return new(big.Rat).SetInt64(v)
	case json.Number:
		n, ok := new(big.Int).SetString(string(v), 10)
		if ok {
			return new(big.Rat).SetInt(n)
		}
	case float64:
		return new(big.Rat).SetFloat64(v)
	}
	return nil
}

func sameFloatKey(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }
