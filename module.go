// Package caddymirror provides a Caddy reverse_proxy transport module that
// duplicates ("mirrors") requests to one or more secondary upstreams while
// forwarding the real request/response through an underlying HTTP transport.
//
// Mirror responses are always discarded; mirroring never affects the
// primary request's outcome, latency budget beyond the initial dispatch, or
// error handling.
package caddymirror

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&MirrorTransport{})
}

// DefaultMaxBodySize is used when MaxBodySize is left unset (zero).
const DefaultMaxBodySize = 2 << 20 // 2 MiB

// DefaultTimeout is used when Timeout is left unset (zero).
const DefaultTimeout = 5 * time.Second

// MirrorTransport is a Caddy http.RoundTripper that forwards the request to
// the primary upstream as usual (via an embedded HTTPTransport), and
// additionally fires copies of the request at zero or more mirror
// upstreams. Mirror requests are best-effort, fire-and-forget: their
// responses are discarded and their errors never affect the primary
// request/response.
type MirrorTransport struct {
	// Mirrors is the list of upstreams that receive a duplicated copy of
	// each request. An empty list makes this transport a zero-overhead
	// passthrough to the primary transport.
	Mirrors []*MirrorUpstream `json:"mirrors,omitempty"`

	// MaxBodySize is the maximum number of request body bytes that will be
	// buffered in order to duplicate them to mirrors. Requests with a
	// larger body are still forwarded to the primary upstream normally,
	// but are mirrored without a body. Default: 2MiB. A value of -1 means
	// unlimited (buffers the entire body in memory).
	MaxBodySize int64 `json:"max_body_size,omitempty"`

	// Timeout bounds how long a single mirror request is allowed to run,
	// independent of the primary request's context. Default: 5s.
	Timeout caddy.Duration `json:"timeout,omitempty"`

	// Headers manipulates the headers sent to every mirror (not the
	// primary upstream). Values may use Caddy placeholders such as
	// {env.MY_SECRET} to inject secrets from the environment without
	// putting them in the config file directly.
	Headers *headers.HeaderOps `json:"headers,omitempty"`

	primary  http.RoundTripper
	client   *http.Client
	logger   *zap.Logger
	rand     *rand.Rand
	replacer *caddy.Replacer
}

// CaddyModule returns the Caddy module information.
func (*MirrorTransport) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.reverse_proxy.transport.mirror",
		New: func() caddy.Module { return new(MirrorTransport) },
	}
}

// Provision sets up the primary transport, mirror HTTP client, and defaults.
func (m *MirrorTransport) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()

	if m.MaxBodySize == 0 {
		m.MaxBodySize = DefaultMaxBodySize
	}
	if m.Timeout == 0 {
		m.Timeout = caddy.Duration(DefaultTimeout)
	}

	primary := new(reverseproxy.HTTPTransport)
	if err := primary.Provision(ctx); err != nil {
		return fmt.Errorf("provisioning primary transport: %w", err)
	}
	m.primary = primary

	for i, mu := range m.Mirrors {
		if err := mu.provision(); err != nil {
			return fmt.Errorf("mirror %d (%s): %w", i, mu.To, err)
		}
	}

	if err := m.Headers.Provision(ctx); err != nil {
		return fmt.Errorf("provisioning mirror headers: %w", err)
	}

	m.client = &http.Client{Transport: &http.Transport{}}
	m.rand = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // sampling only, not security-sensitive
	// caddy.NewReplacer includes the global {env.*} placeholder (backed by
	// os.Getenv) with no request-specific state, so it's safe to share
	// across concurrent mirror goroutines.
	m.replacer = caddy.NewReplacer()

	return nil
}

// Validate ensures the configuration is sound.
func (m *MirrorTransport) Validate() error {
	if m.MaxBodySize < -1 {
		return fmt.Errorf("max_body_size must be -1 (unlimited) or >= 0, got %d", m.MaxBodySize)
	}
	for i, mu := range m.Mirrors {
		if mu.To == "" {
			return fmt.Errorf("mirror %d: 'to' address is required", i)
		}
		if mu.Percent != nil && (*mu.Percent < 0 || *mu.Percent > 100) {
			return fmt.Errorf("mirror %d (%s): percent must be between 0 and 100, got %d", i, mu.To, *mu.Percent)
		}
	}
	return nil
}

// Cleanup closes idle connections held by the primary and mirror transports.
func (m *MirrorTransport) Cleanup() error {
	if cleaner, ok := m.primary.(caddy.CleanerUpper); ok {
		_ = cleaner.Cleanup()
	}
	if m.client != nil {
		m.client.CloseIdleConnections()
	}
	return nil
}

// RoundTrip implements http.RoundTripper. It forwards req to the primary
// transport and, for each configured mirror selected by its sampling
// percentage, fires a best-effort duplicate of req in the background.
func (m *MirrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(m.Mirrors) == 0 {
		return m.primary.RoundTrip(req)
	}

	selected := m.selectMirrors()
	if len(selected) == 0 {
		return m.primary.RoundTrip(req)
	}

	body, bodyTruncated, err := m.bufferBody(req)
	if err != nil {
		return nil, fmt.Errorf("buffering request body for mirroring: %w", err)
	}

	// Snapshot the request fields the mirror goroutines need *before*
	// handing req to the primary transport, which mutates req (e.g.
	// HTTPTransport.SetScheme rewrites req.URL) concurrently with our
	// background sends.
	snap := requestSnapshot{
		method: req.Method,
		url:    *req.URL,
		header: req.Header.Clone(),
	}

	for _, mu := range selected {
		var mirrorBody []byte
		if !bodyTruncated {
			mirrorBody = body
		}
		go m.fireMirror(snap, mu, mirrorBody)
	}

	return m.primary.RoundTrip(req)
}

// requestSnapshot holds the request fields needed to build mirror requests,
// captured before the primary request is handed to its transport so
// background mirror goroutines never read fields concurrently mutated by
// the primary round trip.
type requestSnapshot struct {
	method string
	url    url.URL
	header http.Header
}

// selectMirrors samples m.Mirrors by their configured percentages.
func (m *MirrorTransport) selectMirrors() []*MirrorUpstream {
	var selected []*MirrorUpstream
	for _, mu := range m.Mirrors {
		pct := mu.percent()
		if pct >= 100 || m.rand.Intn(100) < pct {
			selected = append(selected, mu)
		}
	}
	return selected
}

// bufferBody reads up to m.MaxBodySize+1 bytes of req.Body, restores
// req.Body so the primary request is unaffected, and reports whether the
// body was too large to mirror in full (in which case it was NOT
// truncated on the primary request, only omitted from mirrors).
func (m *MirrorTransport) bufferBody(req *http.Request) (body []byte, truncated bool, err error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, false, nil
	}
	if m.MaxBodySize == -1 {
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, false, err
		}
		req.Body = io.NopCloser(bytes.NewReader(buf))
		return buf, false, nil
	}

	limited := io.LimitReader(req.Body, m.MaxBodySize+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}

	if int64(len(buf)) > m.MaxBodySize {
		// Too big to mirror; reconstruct the full body for the primary
		// request from the part we already read plus the remainder.
		req.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), req.Body), req.Body}
		return nil, true, nil
	}

	req.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, false, nil
}

// fireMirror sends a best-effort duplicate of the snapshotted request to
// mu, discarding the response. Errors are logged but never propagated.
func (m *MirrorTransport) fireMirror(snap requestSnapshot, mu *MirrorUpstream, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.Timeout))
	defer cancel()

	mirrorReq, err := m.buildMirrorRequest(ctx, snap, mu, body)
	if err != nil {
		m.logger.Debug("failed to build mirror request",
			zap.String("to", mu.To), zap.Error(err))
		return
	}

	resp, err := m.client.Do(mirrorReq)
	if err != nil {
		m.logger.Debug("mirror request failed",
			zap.String("to", mu.To), zap.Error(err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// buildMirrorRequest constructs a request to mu based on snap, rewriting
// only the scheme/host, and preserving method, path, query, and headers.
func (m *MirrorTransport) buildMirrorRequest(ctx context.Context, snap requestSnapshot, mu *MirrorUpstream, body []byte) (*http.Request, error) {
	dest := snap.url
	dest.Scheme = mu.url.Scheme
	dest.Host = mu.url.Host

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	mirrorReq, err := http.NewRequestWithContext(ctx, snap.method, dest.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	// Clone again per-mirror: snap.header is shared across all mirror
	// goroutines for this request, and we mutate our own copy below.
	mirrorReq.Header = snap.header.Clone()
	mirrorReq.Host = mu.url.Host
	if body != nil {
		mirrorReq.ContentLength = int64(len(body))
	} else {
		mirrorReq.ContentLength = 0
		mirrorReq.Header.Del("Content-Length")
	}
	// m.Headers is nil-safe: applies configured add/set/delete operations
	// (e.g. injecting a secret via {env.VAR}) to this mirror's headers
	// only; the primary request's headers are never touched.
	m.Headers.ApplyTo(mirrorReq.Header, m.replacer)
	return mirrorReq, nil
}

var (
	_ caddy.Module       = (*MirrorTransport)(nil)
	_ caddy.Provisioner  = (*MirrorTransport)(nil)
	_ caddy.Validator    = (*MirrorTransport)(nil)
	_ caddy.CleanerUpper = (*MirrorTransport)(nil)
	_ http.RoundTripper  = (*MirrorTransport)(nil)
)
