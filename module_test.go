package caddymirror

import (
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	"go.uber.org/zap"
)

// newTestTransport builds a MirrorTransport without going through the full
// Caddy provisioning lifecycle, wiring just enough (primary transport,
// mirror client, logger, rand) to exercise RoundTrip directly. The primary
// transport is the real http.DefaultTransport, since these tests only talk
// to local httptest servers.
func newTestTransport(t *testing.T, mirrors ...*MirrorUpstream) *MirrorTransport {
	t.Helper()
	for _, mu := range mirrors {
		if err := mu.provision(); err != nil {
			t.Fatalf("provisioning mirror %q: %v", mu.To, err)
		}
	}
	return &MirrorTransport{
		Mirrors:     mirrors,
		MaxBodySize: DefaultMaxBodySize,
		Timeout:     caddy.Duration(2 * time.Second),
		primary:     http.DefaultTransport,
		client:      &http.Client{Transport: &http.Transport{}},
		logger:      zap.NewNop(),
		rand:        rand.New(rand.NewSource(1)),
		replacer:    caddy.NewReplacer(),
	}
}

func TestRoundTripNoMirrorsIsPassthrough(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("primary"))
	}))
	defer primary.Close()

	m := newTestTransport(t)

	req := newRequestTo(t, primary.URL, "body")
	resp, err := m.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "primary" {
		t.Errorf("got %q, want %q", body, "primary")
	}
}

func TestRoundTripMirrorsFullBody(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("primary"))
	}))
	defer primary.Close()

	received := make(chan string, 1)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer mirror.Close()

	mu := &MirrorUpstream{To: mirror.URL, Percent: intPtr(100)}
	m := newTestTransport(t, mu)

	req := newRequestTo(t, primary.URL, "hello mirror")
	resp, err := m.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "primary" {
		t.Errorf("primary response = %q, want %q (mirror must never affect primary)", body, "primary")
	}

	select {
	case got := <-received:
		if got != "hello mirror" {
			t.Errorf("mirror received body %q, want %q", got, "hello mirror")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mirror never received a request")
	}
}

func TestRoundTripInjectsHeadersWithEnvSecret(t *testing.T) {
	t.Setenv("MIRROR_TEST_SECRET", "super-secret-value")

	primaryHeaders := make(chan http.Header, 1)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHeaders <- r.Header.Clone()
		w.Write([]byte("primary"))
	}))
	defer primary.Close()

	mirrorHeaders := make(chan http.Header, 1)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHeaders <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	mu := &MirrorUpstream{To: mirror.URL, Percent: intPtr(100)}
	m := newTestTransport(t, mu)
	m.Headers = &headers.HeaderOps{
		Set:    http.Header{"X-Api-Key": []string{"{env.MIRROR_TEST_SECRET}"}},
		Add:    http.Header{"X-Mirrored-From": []string{"primary"}},
		Delete: []string{"Authorization"},
	}

	req := newRequestTo(t, primary.URL, "body")
	req.Header.Set("Authorization", "Bearer client-token")

	resp, err := m.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	select {
	case got := <-primaryHeaders:
		if got.Get("Authorization") != "Bearer client-token" {
			t.Errorf("primary Authorization = %q, want unchanged %q", got.Get("Authorization"), "Bearer client-token")
		}
		if got.Get("X-Api-Key") != "" || got.Get("X-Mirrored-From") != "" {
			t.Errorf("primary received mirror-only injected headers: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primary never received a request")
	}

	select {
	case got := <-mirrorHeaders:
		if got.Get("X-Api-Key") != "super-secret-value" {
			t.Errorf("mirror X-Api-Key = %q, want the {env.*} placeholder expanded to %q", got.Get("X-Api-Key"), "super-secret-value")
		}
		if got.Get("X-Mirrored-From") != "primary" {
			t.Errorf("mirror X-Mirrored-From = %q, want %q", got.Get("X-Mirrored-From"), "primary")
		}
		if got.Get("Authorization") != "" {
			t.Errorf("mirror Authorization = %q, want deleted (empty)", got.Get("Authorization"))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mirror never received a request")
	}
}

func TestRoundTripOversizedBodySkipsMirrorButNotPrimary(t *testing.T) {
	var primaryReceived atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		primaryReceived.Store(int64(len(body)))
		w.Write([]byte("primary"))
	}))
	defer primary.Close()

	var mirrorCalled atomic.Bool
	var mirrorBodyLen atomic.Int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorCalled.Store(true)
		body, _ := io.ReadAll(r.Body)
		mirrorBodyLen.Store(int64(len(body)))
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	mu := &MirrorUpstream{To: mirror.URL, Percent: intPtr(100)}
	m := newTestTransport(t, mu)
	m.MaxBodySize = 4 // smaller than the body we send

	bigBody := strings.Repeat("x", 100)
	req := newRequestTo(t, primary.URL, bigBody)
	resp, err := m.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if got := primaryReceived.Load(); got != int64(len(bigBody)) {
		t.Errorf("primary received %d bytes, want %d (oversized body must still be forwarded in full)", got, len(bigBody))
	}

	// Give the (should-not-happen) mirror goroutine a moment, then assert
	// it was never called with a body.
	time.Sleep(100 * time.Millisecond)
	if mirrorCalled.Load() && mirrorBodyLen.Load() != 0 {
		t.Errorf("mirror received a body of length %d, want empty body when oversized", mirrorBodyLen.Load())
	}
}

func TestSelectMirrorsSampling(t *testing.T) {
	always := &MirrorUpstream{To: "http://a", Percent: intPtr(100)}
	never := &MirrorUpstream{To: "http://b", Percent: intPtr(0)}
	for _, mu := range []*MirrorUpstream{always, never} {
		if err := mu.provision(); err != nil {
			t.Fatalf("provision: %v", err)
		}
	}
	m := &MirrorTransport{
		Mirrors: []*MirrorUpstream{always, never},
		rand:    rand.New(rand.NewSource(1)),
	}

	for i := 0; i < 20; i++ {
		selected := m.selectMirrors()
		if len(selected) != 1 || selected[0] != always {
			t.Fatalf("iteration %d: selectMirrors() = %v, want only [always]", i, selected)
		}
	}
}

func TestHasScheme(t *testing.T) {
	cases := map[string]bool{
		"http://mirror:9090":  true,
		"https://mirror:9090": true,
		"mirror:9090":         false, // bare host:port, no scheme
		"127.0.0.1:9090":      false,
		"mirror":              false,
	}
	for in, want := range cases {
		if got := hasScheme(in); got != want {
			t.Errorf("hasScheme(%q) = %v, want %v", in, got, want)
		}
	}
}

func intPtr(i int) *int { return &i }

func newRequestTo(t *testing.T, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}
