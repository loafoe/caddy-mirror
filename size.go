package caddymirror

import (
	"fmt"
	"strconv"
	"strings"
)

// parseSize parses a byte size like "2MB", "512KB", or a plain integer
// number of bytes. Units are binary (1KB = 1024 bytes) and case-insensitive.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	units := []struct {
		suffix string
		mult   int64
	}{
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
		{"B", 1},
	}

	upper := strings.ToUpper(s)
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			numPart := strings.TrimSpace(s[:len(s)-len(u.suffix)])
			if numPart == "" {
				return 0, fmt.Errorf("missing number before unit %q", u.suffix)
			}
			n, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid number %q: %w", numPart, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("size must not be negative")
			}
			return int64(n * float64(u.mult)), nil
		}
	}

	// Plain integer bytes.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a number optionally followed by KB/MB/GB", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must not be negative")
	}
	return n, nil
}
