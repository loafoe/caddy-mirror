package caddymirror

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/caddytest"
)

// TestIntegrationMirrorsAndPassesThroughPrimary spins up a real Caddy
// instance from a Caddyfile using `transport mirror`, verifies the client
// receives the primary backend's response, and that the mirror backend
// also received a duplicate of the request.
func TestIntegrationMirrorsAndPassesThroughPrimary(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from primary"))
	}))
	defer primary.Close()

	var mu sync.Mutex
	var mirrorReceived bool
	var mirrorBody string
	mirrorReady := make(chan struct{}, 1)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		mirrorReceived = true
		mirrorBody = string(body)
		mu.Unlock()
		select {
		case mirrorReady <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusTeapot) // response must be discarded/ignored
	}))
	defer mirror.Close()

	tester := caddytest.NewTester(t)
	tester.InitServer(fmt.Sprintf(`
	{
		admin localhost:2999
		http_port 9080
	}
	localhost:9080 {
		reverse_proxy %s {
			transport mirror {
				to %s
				timeout 2s
			}
		}
	}
	`, primary.Listener.Addr().String(), mirror.Listener.Addr().String()), "caddyfile")

	req, err := http.NewRequest(http.MethodPost, "http://localhost:9080/foo", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp := tester.AssertResponseCode(req, http.StatusOK)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if string(respBody) != "hello from primary" {
		t.Errorf("client got %q, want %q", respBody, "hello from primary")
	}

	select {
	case <-mirrorReady:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mirror to receive request")
	}

	mu.Lock()
	defer mu.Unlock()
	if !mirrorReceived {
		t.Error("mirror backend did not receive a duplicated request")
	}
	if mirrorBody != "payload" {
		t.Errorf("mirror received body %q, want %q", mirrorBody, "payload")
	}
}
