package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ByteSize wraps a byte count so config fields can be written with a
// human-readable unit suffix ("10MB") instead of a raw integer.
type ByteSize int64

func (b ByteSize) Int64() int64 { return int64(b) }

// unit suffixes, longest first so "MB" is matched before the trailing "B".
var byteSizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TB", 1 << 40},
	{"GB", 1 << 30},
	{"MB", 1 << 20},
	{"KB", 1 << 10},
	{"B", 1},
}

func parseByteSize(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size value")
	}
	upper := strings.ToUpper(trimmed)
	for _, u := range byteSizeUnits {
		if !strings.HasSuffix(upper, u.suffix) {
			continue
		}
		numPart := strings.TrimSpace(strings.TrimSuffix(upper, u.suffix))
		if numPart == "" {
			continue
		}
		val, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q", s)
		}
		if val < 0 {
			return 0, fmt.Errorf("size %q must not be negative", s)
		}
		return int64(val * float64(u.mult)), nil
	}
	val, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a number or a value like \"10MB\"", s)
	}
	return val, nil
}

func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("size must be a string like \"10MB\": %w", err)
	}
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	*b = ByteSize(n)
	return nil
}
