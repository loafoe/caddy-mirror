package caddymirror

import (
	"fmt"
	"net/url"
	"strings"
)

// MirrorUpstream is a single mirror target: an address that receives a
// sampled duplicate of each request.
type MirrorUpstream struct {
	// To is the base URL of the mirror upstream, e.g. "http://mirror:9090".
	// If no scheme is given, "http://" is assumed.
	To string `json:"to"`

	// Percent is the percentage (0-100) of requests duplicated to this
	// mirror. Unset (nil) defaults to 100 (mirror every request); an
	// explicit 0 disables this mirror without removing its config.
	Percent *int `json:"percent,omitempty"`

	url *url.URL
}

// percent returns the effective sampling percentage, applying the default
// of 100 when unset. Only valid after provision has run.
func (mu *MirrorUpstream) percent() int {
	if mu.Percent == nil {
		return 100
	}
	return *mu.Percent
}

func (mu *MirrorUpstream) provision() error {

	raw := mu.To
	if !hasScheme(raw) {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid mirror address %q: %w", mu.To, err)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid mirror address %q: no host", mu.To)
	}
	mu.url = u
	return nil
}

func hasScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	// Everything before "://" must look like a scheme (letters, digits,
	// '+', '-', '.'), otherwise a bare "host:port/..." with a slashed
	// path could false-positive (e.g. "127.0.0.1:9090/a://b").
	for _, c := range s[:i] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '+', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}
