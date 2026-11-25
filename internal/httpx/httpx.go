// Package httpx provides tuned HTTP transports, connection-reuse helpers,
// bounded memory-efficient response readers, and httptrace-based slow request
// observability shared across Xiaomu's external network clients.
package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo/netproxy"
)

const (
	// DefaultSlowThreshold is the total request duration (including body transfer)
	// above which TracingRoundTripper emits a structured slow-request log.
	DefaultSlowThreshold = 500 * time.Millisecond

	// DefaultClientTimeout is the fallback total timeout when NewClient receives
	// a non-positive timeout, preventing unbounded hangs.
	DefaultClientTimeout = 20 * time.Second

	// DefaultMaxDrainBytes caps how many unread response bytes DrainAndClose will
	// discard before closing the body. Small error pages and trailing bytes are
	// drained so HTTP/1.1 keep-alive connections return to the idle pool, while
	// large or streaming bodies are closed immediately without stalling.
	DefaultMaxDrainBytes = 256 << 10 // 256 KiB

	DefaultDialTimeout          = 10 * time.Second
	DefaultKeepAlive            = 30 * time.Second
	DefaultTLSHandshakeTimeout  = 10 * time.Second
	DefaultIdleConnTimeout      = 90 * time.Second
	DefaultMaxIdleConns         = 128
	DefaultMaxIdleConnsPerHost  = 16
	DefaultMaxConnsPerHost      = 64
	DefaultExpectContinuePeriod = 1 * time.Second
)

// TraceStats captures per-phase timings for a single external HTTP request.
type TraceStats struct {
	Method     string
	URL        string // sanitized: scheme://host/path (credentials, query and fragment stripped)
	Host       string
	StatusCode int
	Reused     bool
	WasIdle    bool
	IdleTime   time.Duration
	BytesRead  int64
	DNS        time.Duration
	TCP        time.Duration
	TLS        time.Duration
	TTFB       time.Duration
	Transfer   time.Duration
	Total      time.Duration
	Err        error
}

// TracingOptions configures TracingRoundTripper.
type TracingOptions struct {
	// Threshold controls when a slow-request warning is logged.
	// Non-positive values default to DefaultSlowThreshold (500ms).
	Threshold time.Duration
	// Logger receives structured slow-request logs. Nil defaults to a JSON
	// slog.Logger writing to os.Stderr.
	Logger *slog.Logger
	// OnSlow is an optional callback invoked whenever a request meets or exceeds
	// Threshold, useful for metrics export or testing.
	OnSlow func(TraceStats)
	// AllowCloseIdle permits CloseIdleConnections to forward to Base. Keep false
	// for process-wide shared transports so one caller cannot flush another's pool.
	AllowCloseIdle bool
}

// TracingRoundTripper is an http.RoundTripper middleware that attaches
// net/http/httptrace hooks to measure DNS, TCP, TLS, TTFB, and body transfer
// durations, emitting a structured slow log when total latency exceeds Threshold.
type TracingRoundTripper struct {
	Base           http.RoundTripper
	Threshold      time.Duration
	Logger         *slog.Logger
	OnSlow         func(TraceStats)
	AllowCloseIdle bool
}

var (
	defaultLogger = sync.OnceValue(func() *slog.Logger {
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelWarn,
		}))
	})

	sharedRoundTripper = sync.OnceValue(func() http.RoundTripper {
		return WrapRoundTripper(NewTransport(), TracingOptions{
			Threshold:      DefaultSlowThreshold,
			AllowCloseIdle: false,
		})
	})

	sharedStreamClient = sync.OnceValue(func() *http.Client {
		return NewStreamClient(30*time.Second, false)
	})
)

// NewTransport clones MyGo's proxy-aware transport (preserving system proxy
// and PAC resolution) and tunes connection pooling and dial timeouts for high
// concurrency. Unlike http.DefaultTransport (whose MaxIdleConnsPerHost is 2),
// it keeps up to 16 idle connections per host and bounds connect waits to 10s.
func NewTransport() *http.Transport {
	t := netproxy.Transport().Clone()
	t.DialContext = (&net.Dialer{
		Timeout:   DefaultDialTimeout,
		KeepAlive: DefaultKeepAlive,
	}).DialContext
	t.ForceAttemptHTTP2 = true
	t.MaxIdleConns = DefaultMaxIdleConns
	t.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	t.MaxConnsPerHost = DefaultMaxConnsPerHost
	t.IdleConnTimeout = DefaultIdleConnTimeout
	t.TLSHandshakeTimeout = DefaultTLSHandshakeTimeout
	t.ExpectContinueTimeout = DefaultExpectContinuePeriod
	return t
}

// SharedRoundTripper returns the process-wide pooled and traced RoundTripper.
func SharedRoundTripper() http.RoundTripper {
	return sharedRoundTripper()
}

// WrapRoundTripper wraps base with httptrace phase timing and slow-request logging.
func WrapRoundTripper(base http.RoundTripper, opts TracingOptions) *TracingRoundTripper {
	if base == nil {
		base = NewTransport()
	}
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = DefaultSlowThreshold
	}
	return &TracingRoundTripper{
		Base:           base,
		Threshold:      threshold,
		Logger:         opts.Logger,
		OnSlow:         opts.OnSlow,
		AllowCloseIdle: opts.AllowCloseIdle,
	}
}

// NewClient returns an http.Client backed by the shared pooled/traced transport
// with an explicit total timeout.
func NewClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultClientTimeout
	}
	return &http.Client{
		Transport: SharedRoundTripper(),
		Timeout:   timeout,
	}
}

// NewClientWithJar returns an http.Client with an isolated CookieJar while
// still sharing the pooled/traced underlying transport.
func NewClientWithJar(timeout time.Duration, jar http.CookieJar) *http.Client {
	c := NewClient(timeout)
	c.Jar = jar
	return c
}

// NewStreamClient builds an http.Client suitable for long-lived media streams
// or large binary downloads where a total Client.Timeout would prematurely cut
// off healthy transfers. It bounds connection/TLS/header waits while owning an
// isolated pooled transport whose idle connections can be closed on shutdown.
func NewStreamClient(responseHeaderTimeout time.Duration, disableCompression bool) *http.Client {
	t := NewTransport()
	t.DisableCompression = disableCompression
	if responseHeaderTimeout > 0 {
		t.ResponseHeaderTimeout = responseHeaderTimeout
	}
	return &http.Client{
		Transport: WrapRoundTripper(t, TracingOptions{
			Threshold:      DefaultSlowThreshold,
			AllowCloseIdle: true,
		}),
	}
}

// SharedStreamClient returns a shared streaming client with a 30s response
// header timeout, used as a safe fallback instead of http.DefaultClient.
func SharedStreamClient() *http.Client {
	return sharedStreamClient()
}

// CloseIdleConnections forwards to the underlying transport when permitted.
func (t *TracingRoundTripper) CloseIdleConnections() {
	if !t.AllowCloseIdle || t.Base == nil {
		return
	}
	if ci, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		ci.CloseIdleConnections()
	}
}

type traceState struct {
	mu sync.Mutex

	reqStart time.Time

	dnsStart time.Time
	dnsDur   time.Duration

	// Track concurrent dual-stack (Happy Eyeballs) dials keyed by network|addr.
	connectStarts map[string]time.Time
	tcpDur        time.Duration
	tcpSucceeded  bool

	tlsStart time.Time
	tlsDur   time.Duration

	gotConn   time.Time
	reused    bool
	wasIdle   bool
	idleTime  time.Duration
	wroteReq  time.Time
	firstByte time.Time

	headersDone time.Time
}

func (s *traceState) snapshot(end time.Time, method, sanitizedURL, host string, status int, bytesRead int64, err error) TraceStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	dns := s.dnsDur
	if dns == 0 && !s.dnsStart.IsZero() {
		dns = clampDuration(end.Sub(s.dnsStart))
	}

	tcp := s.tcpDur
	if tcp == 0 && len(s.connectStarts) > 0 {
		var earliest time.Time
		for _, started := range s.connectStarts {
			if earliest.IsZero() || started.Before(earliest) {
				earliest = started
			}
		}
		if !earliest.IsZero() {
			tcp = clampDuration(end.Sub(earliest))
		}
	}

	tls := s.tlsDur
	if tls == 0 && !s.tlsStart.IsZero() {
		tls = clampDuration(end.Sub(s.tlsStart))
	}

	// TTFB measures server processing wait: from request write completion (or
	// connection acquisition if WroteRequest did not fire) until the first
	// response byte arrives. If the request failed while waiting for headers,
	// attribute the wait up to end.
	ttfbOrigin := s.wroteReq
	if ttfbOrigin.IsZero() {
		ttfbOrigin = s.gotConn
	}
	var ttfb time.Duration
	if !s.firstByte.IsZero() {
		if ttfbOrigin.IsZero() {
			ttfbOrigin = s.reqStart
		}
		ttfb = clampDuration(s.firstByte.Sub(ttfbOrigin))
	} else if !ttfbOrigin.IsZero() && s.tlsStart.IsZero() {
		ttfb = clampDuration(end.Sub(ttfbOrigin))
	}

	var transfer time.Duration
	if !s.firstByte.IsZero() {
		transfer = clampDuration(end.Sub(s.firstByte))
	} else if !s.headersDone.IsZero() {
		transfer = clampDuration(end.Sub(s.headersDone))
	}

	return TraceStats{
		Method:     method,
		URL:        sanitizedURL,
		Host:       host,
		StatusCode: status,
		Reused:     s.reused,
		WasIdle:    s.wasIdle,
		IdleTime:   s.idleTime,
		BytesRead:  bytesRead,
		DNS:        dns,
		TCP:        tcp,
		TLS:        tls,
		TTFB:       ttfb,
		Transfer:   transfer,
		Total:      clampDuration(end.Sub(s.reqStart)),
		Err:        err,
	}
}

func buildClientTrace(state *traceState) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			now := time.Now()
			state.mu.Lock()
			state.dnsStart = now
			state.mu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			now := time.Now()
			state.mu.Lock()
			if !state.dnsStart.IsZero() {
				state.dnsDur += clampDuration(now.Sub(state.dnsStart))
				state.dnsStart = time.Time{}
			}
			state.mu.Unlock()
		},
		ConnectStart: func(network, addr string) {
			now := time.Now()
			key := network + "|" + addr
			state.mu.Lock()
			if state.connectStarts == nil {
				state.connectStarts = make(map[string]time.Time, 2)
			}
			state.connectStarts[key] = now
			state.mu.Unlock()
		},
		ConnectDone: func(network, addr string, err error) {
			now := time.Now()
			key := network + "|" + addr
			state.mu.Lock()
			if started, ok := state.connectStarts[key]; ok {
				delete(state.connectStarts, key)
				d := clampDuration(now.Sub(started))
				if err == nil {
					state.tcpDur = d
					state.tcpSucceeded = true
				} else if !state.tcpSucceeded && d > state.tcpDur {
					state.tcpDur = d
				}
			}
			state.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			now := time.Now()
			state.mu.Lock()
			state.tlsStart = now
			state.mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			now := time.Now()
			state.mu.Lock()
			if !state.tlsStart.IsZero() {
				state.tlsDur += clampDuration(now.Sub(state.tlsStart))
				state.tlsStart = time.Time{}
			}
			state.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			now := time.Now()
			state.mu.Lock()
			state.gotConn = now
			state.reused = info.Reused
			state.wasIdle = info.WasIdle
			state.idleTime = info.IdleTime
			state.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			now := time.Now()
			state.mu.Lock()
			state.wroteReq = now
			state.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			now := time.Now()
			state.mu.Lock()
			if state.firstByte.IsZero() {
				state.firstByte = now
			}
			state.mu.Unlock()
		},
	}
}

// RoundTrip executes req with httptrace hooks attached and tracks body transfer
// completion so slow logs cover the full lifecycle including response streaming.
func (t *TracingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = SharedRoundTripper()
	}
	state := &traceState{reqStart: time.Now()}
	trace := buildClientTrace(state)

	ctx := req.Context()
	tracedReq := req.WithContext(httptrace.WithClientTrace(ctx, trace))
	resp, err := base.RoundTrip(tracedReq)
	headersDone := time.Now()

	method := req.Method
	sanitized := SanitizeURL(req.URL)
	host := ""
	if req.URL != nil {
		host = req.URL.Host
	}

	if err != nil || resp == nil {
		t.record(ctx, state.snapshot(headersDone, method, sanitized, host, 0, 0, err))
		return resp, err
	}

	state.mu.Lock()
	state.headersDone = headersDone
	state.mu.Unlock()

	if resp.Body == nil || resp.Body == http.NoBody {
		t.record(ctx, state.snapshot(headersDone, method, sanitized, host, resp.StatusCode, 0, nil))
		return resp, nil
	}

	resp.Body = &trackedBody{
		ReadCloser: resp.Body,
		onDone: func(bytesRead int64, readErr error) {
			t.record(ctx, state.snapshot(time.Now(), method, sanitized, host, resp.StatusCode, bytesRead, readErr))
		},
	}
	return resp, nil
}

func (t *TracingRoundTripper) record(ctx context.Context, stats TraceStats) {
	threshold := t.Threshold
	if threshold <= 0 {
		threshold = DefaultSlowThreshold
	}
	if stats.Total < threshold {
		return
	}
	logger := t.Logger
	if logger == nil {
		logger = defaultLogger()
	}
	attrs := []any{
		slog.String("method", stats.Method),
		slog.String("url", stats.URL),
		slog.String("host", stats.Host),
		slog.Int("status", stats.StatusCode),
		slog.Bool("conn_reused", stats.Reused),
		slog.Int64("bytes", stats.BytesRead),
		slog.Float64("dns_ms", toMillis(stats.DNS)),
		slog.Float64("tcp_ms", toMillis(stats.TCP)),
		slog.Float64("tls_ms", toMillis(stats.TLS)),
		slog.Float64("ttfb_ms", toMillis(stats.TTFB)),
		slog.Float64("transfer_ms", toMillis(stats.Transfer)),
		slog.Float64("total_ms", toMillis(stats.Total)),
	}
	if stats.Err != nil {
		attrs = append(attrs, slog.String("error", stats.Err.Error()))
	}
	// Use WithoutCancel so slow logs are never dropped when ctx itself timed out.
	logger.WarnContext(context.WithoutCancel(ctx), "slow external http request", attrs...)
	if t.OnSlow != nil {
		t.OnSlow(stats)
	}
}

type trackedBody struct {
	io.ReadCloser
	once      sync.Once
	mu        sync.Mutex
	bytesRead int64
	onDone    func(bytesRead int64, readErr error)
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		b.bytesRead += int64(n)
		b.mu.Unlock()
	}
	if err != nil {
		reportErr := err
		if errors.Is(err, io.EOF) {
			reportErr = nil
		}
		b.finish(reportErr)
	}
	return n, err
}

func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(nil)
	return err
}

func (b *trackedBody) finish(readErr error) {
	b.once.Do(func() {
		b.mu.Lock()
		n := b.bytesRead
		b.mu.Unlock()
		if b.onDone != nil {
			b.onDone(n, readErr)
		}
	})
}

// DrainAndClose drains up to DefaultMaxDrainBytes of unread data from body
// before closing it, allowing HTTP/1.1 keep-alive connections to be reused by
// the Transport's idle pool without risking unbounded reads on huge streams.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, DefaultMaxDrainBytes))
	_ = body.Close()
}

// ReadLimit reads up to limit bytes from r without the repeated slice
// reallocations of io.ReadAll when contentLength is known.
//
// If contentLength > limit, or if r yields more than limit bytes, exceeded is
// true and no more than limit+1 bytes are read. When contentLength is known and
// <= limit, a single buffer of size contentLength+1 is allocated so the final
// Read observes io.EOF cleanly for HTTP connection reuse.
func ReadLimit(r io.Reader, contentLength, limit int64) (data []byte, exceeded bool, err error) {
	if limit < 0 {
		limit = 0
	}
	if contentLength > limit {
		return nil, true, nil
	}
	const (
		defaultInitCap   = 8 << 10
		maxPreallocBytes = 2 << 20 // cap single-shot prealloc so spoofed Content-Length cannot OOM
	)
	capHint := int64(defaultInitCap)
	if contentLength > 0 {
		capHint = min(contentLength+1, maxPreallocBytes)
	}
	if capHint > limit+1 {
		capHint = limit + 1
	}
	if capHint < 1 {
		capHint = 1
	}

	buf := make([]byte, 0, int(capHint))
	maxRead := limit + 1
	for {
		if int64(len(buf)) == maxRead {
			return buf[:limit], true, nil
		}
		if len(buf) == cap(buf) {
			nextCap := int64(cap(buf)) * 2
			if nextCap < defaultInitCap {
				nextCap = defaultInitCap
			}
			if nextCap > maxRead {
				nextCap = maxRead
			}
			grown := make([]byte, len(buf), int(nextCap))
			copy(grown, buf)
			buf = grown
		}
		n, rerr := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if int64(len(buf)) > limit {
			return buf[:limit], true, nil
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return buf, false, nil
			}
			return buf, false, rerr
		}
	}
}

// SanitizeURL returns scheme://host/path with userinfo, query parameters, and
// fragments removed so tokens, cookies, or private URLs never leak into logs.
func SanitizeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clean := url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}
	return clean.String()
}

func clampDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func toMillis(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Millisecond)*100) / 100
}
