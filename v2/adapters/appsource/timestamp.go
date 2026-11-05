package appsource

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SafeLoader timestamps can appear in otherwise unused AsyncAPI examples. Keep them typed, not strings,
// without imposing Go's narrower YAML timestamp layouts (for example a space-separated '-5' offset).
func timestamp(s string) (time.Time, error) {
	parts := regexp.MustCompile(`^([0-9]{4})-([0-9]{1,2})-([0-9]{1,2})(?:(?:[Tt]|[ \t]+)([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]*))?(?:[ \t]*(Z|[-+][0-9]{1,2}(?::[0-9]{2})?))?)?$`).FindStringSubmatch(s)
	if parts == nil {
		return time.Time{}, fmt.Errorf("invalid timestamp")
	}
	fields := make([]int, 7)
	for i := 1; i <= 6; i++ {
		fields[i], _ = strconv.Atoi(parts[i])
	}
	location, err := timestampZone(parts[8])
	if err != nil {
		return time.Time{}, err
	}
	fraction := parts[7] + "000000"
	microseconds, _ := strconv.Atoi(fraction[:6])
	value := time.Date(fields[1], time.Month(fields[2]), fields[3], fields[4], fields[5], fields[6], microseconds*1000, location)
	if !sameDate(value, fields) {
		return time.Time{}, fmt.Errorf("invalid timestamp")
	}
	return value, nil
}
func sameDate(t time.Time, f []int) bool {
	return f[1] >= 1 && t.Year() == f[1] && int(t.Month()) == f[2] && t.Day() == f[3] && t.Hour() == f[4] && t.Minute() == f[5] && t.Second() == f[6]
}
func timestampZone(s string) (*time.Location, error) {
	if s == "" || s == "Z" {
		return time.UTC, nil
	}
	parts := strings.Split(s[1:], ":")
	hours, _ := strconv.Atoi(parts[0])
	minutes := 0
	if len(parts) > 1 {
		minutes, _ = strconv.Atoi(parts[1])
	}
	offset := (hours*60 + minutes) * 60
	if offset >= 24*60*60 {
		return nil, fmt.Errorf("invalid timestamp offset")
	}
	if s[0] == '-' {
		offset = -offset
	}
	return time.FixedZone("", offset), nil
}
