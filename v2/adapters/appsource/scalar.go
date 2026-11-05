package appsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Source scalars retain PyYAML's YAML 1.1 resolution. In particular plain 1e3 and 0o10 are strings,
// 012 is octal, 1:20 is an integer, and 1.0e+3 is a float. Flags bytes retain those numeric types.
func scalar(n *yaml.Node) (any, error) {
	tag := n.Tag
	if n.Style == 0 {
		tag = implicitTag(n.Value)
	}
	switch tag {
	case "!!str":
		return n.Value, nil
	case "!!null":
		return nil, nil
	case "!!bool":
		return sourceBool(n.Value)
	case "!!int":
		return sourceInt(n.Value)
	case "!!float":
		return sourceFloat(n.Value)
	case "!!timestamp":
		return timestamp(n.Value)
	case "!!binary":
		var decoded string
		if err := n.Decode(&decoded); err != nil {
			return nil, err
		}
		return []byte(decoded), nil
	default:
		return nil, fmt.Errorf("unsupported YAML tag %s", tag)
	}
}
func implicitTag(s string) string {
	switch s {
	case "yes", "Yes", "YES", "no", "No", "NO", "true", "True", "TRUE", "false", "False", "FALSE", "on", "On", "ON", "off", "Off", "OFF":
		return "!!bool"
	case "null", "Null", "NULL", "~", "":
		return "!!null"
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF", ".nan", ".NaN", ".NAN":
		return "!!float"
	}
	if regexp.MustCompile(`^[-+]?(?:0b[0-1_]+|0[0-7_]+|0x[0-9a-fA-F_]+|0|[1-9][0-9_]*|[1-9][0-9_]*(?::[0-5]?[0-9])+)$`).MatchString(s) {
		return "!!int"
	}
	if regexp.MustCompile(`^(?:[-+]?[0-9][0-9_]*\.[0-9_]*(?:[eE][-+][0-9]+)?|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*)$`).MatchString(s) {
		return "!!float"
	}
	if regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt]|[ 	]+)[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]*)?(?:[ 	]*(?:Z|[-+][0-9]{1,2}(?::[0-9]{2})?))?)$`).MatchString(s) {
		return "!!timestamp"
	}
	return "!!str"
}
func sourceInt(s string) (any, error) {
	s = strings.ReplaceAll(s, "_", "")
	var n *big.Int
	if strings.Contains(s, ":") {
		n = new(big.Int)
		sign := 1
		if strings.HasPrefix(s, "-") {
			sign = -1
		}
		for _, part := range strings.Split(strings.TrimLeft(s, "+-"), ":") {
			v, ok := new(big.Int).SetString(part, 10)
			if !ok {
				return nil, fmt.Errorf("invalid integer")
			}
			n.Mul(n, big.NewInt(60))
			n.Add(n, v)
		}
		if sign < 0 {
			n.Neg(n)
		}
	} else {
		var ok bool
		n, ok = new(big.Int).SetString(s, 0)
		if !ok {
			return nil, fmt.Errorf("invalid integer")
		}
	}
	if n.IsInt64() {
		return n.Int64(), nil
	}
	return json.Number(n.String()), nil
}

func sourceFloat(s string) (float64, error) {
	s = strings.ReplaceAll(s, "_", "")
	switch strings.ToLower(s) {
	case ".inf", "+.inf":
		return math.Inf(1), nil
	case "-.inf":
		return math.Inf(-1), nil
	case ".nan":
		return math.NaN(), nil
	}
	if strings.Contains(s, ":") {
		return sexagesimal(s)
	}
	n, err := strconv.ParseFloat(s, 64)
	if errors.Is(err, strconv.ErrRange) {
		return n, nil
	}
	return n, err
}
func sexagesimal(s string) (float64, error) {
	sign := 1.0
	if strings.HasPrefix(s, "-") {
		sign = -1
	}
	s = strings.TrimLeft(s, "+-")
	n, base := 0.0, 1.0
	parts := strings.Split(s, ":")
	for i := len(parts) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, err
		}
		n += v * base
		base *= 60
	}
	return n * sign, nil
}

func sourceBool(s string) (bool, error) {
	switch s {
	case "yes", "Yes", "YES", "on", "On", "ON", "true", "True", "TRUE":
		return true, nil
	case "no", "No", "NO", "off", "Off", "OFF", "false", "False", "FALSE":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean")
}
