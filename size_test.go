package caddymirror

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"1024", 1024, false},
		{"1KB", 1024, false},
		{"2MB", 2 << 20, false},
		{"1GB", 1 << 30, false},
		{"1.5MB", int64(1.5 * (1 << 20)), false},
		{"", 0, true},
		{"-1MB", 0, true},
		{"MB", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSize(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSize(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
