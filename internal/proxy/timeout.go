package proxy

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Bound each upstream wait, not the length of a movie or a live stream. The
// timer stops while writing to AVPlayer, which may deliberately pause reads.
func (p *Proxy) do(req *http.Request) (*http.Response, error) {
	return p.doWithTimeout(req, 0)
}

func (p *Proxy) doWithTimeout(req *http.Request, limit time.Duration) (*http.Response, error) {
	timeout := p.readTimeout
	if timeout <= 0 {
		timeout = upstreamIdleTimeout
	}
	if limit > 0 && limit < timeout {
		timeout = limit
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	timer := time.AfterFunc(timeout, func() { cancel(errUpstreamTimeout) })
	resp, err := p.client.Do(req.WithContext(ctx))
	timer.Stop()
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		cancel(nil)
		return nil, err
	}
	resp.Body = &idleBody{ReadCloser: resp.Body, ctx: ctx, cancel: cancel, timer: timer, timeout: timeout}
	return resp, nil
}

type idleBody struct {
	io.ReadCloser
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	timeout time.Duration
}

func (b *idleBody) Read(buf []byte) (int, error) {
	b.timer.Reset(b.timeout)
	n, err := b.ReadCloser.Read(buf)
	b.timer.Stop()
	if err != nil {
		if cause := context.Cause(b.ctx); cause != nil {
			err = cause
		}
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	b.cancel(nil)
	return b.ReadCloser.Close()
}
