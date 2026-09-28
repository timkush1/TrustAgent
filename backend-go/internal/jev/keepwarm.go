package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// ErrKeyCheckUnsupported is returned by CheckKey for backends without a key endpoint.
var ErrKeyCheckUnsupported = errors.New("jev: the key check is available for OpenRouter backends only")

// KeyInfo is OpenRouter's GET /api/v1/key data (shape verified in P1g; fields are optional).
type KeyInfo struct {
	Label             string   `json:"label"`
	UsageUSD          float64  `json:"usage"`
	LimitUSD          *float64 `json:"limit"`
	LimitRemainingUSD *float64 `json:"limit_remaining"`
	IsFreeTier        bool     `json:"is_free_tier"`
	RateLimit         *struct {
		Requests int    `json:"requests"`
		Interval string `json:"interval"`
	} `json:"rate_limit,omitempty"`
}

// CheckKey validates the credential with OpenRouter's GET /api/v1/key: free, no decision call.
// Errors use the same taxonomy as decision calls; they are recorded in Status but never touch
// the breaker.
func (c *httpClient) CheckKey(ctx context.Context) (*KeyInfo, error) {
	if c.ep.keyURL == "" {
		return nil, ErrKeyCheckUnsupported
	}
	key, e := c.keys.get(ctx)
	if e != nil {
		return nil, e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ep.keyURL, nil)
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, Message: "cannot build the key check", Err: err}
	}
	c.setHeaders(req.Header, key)
	req.Header.Del("Content-Type")
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Kind: KindTimeout, Message: "the key check did not answer in time", Err: err, Hint: Hint(KindTimeout)}
		}
		return nil, &Error{Kind: KindRetryable, Retryable: true, Message: "network error: " + scrub(unwrapURLError(err).Error()), Err: err, Hint: Hint(KindRetryable)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, &Error{Kind: KindRetryable, Retryable: true, Message: "reading the key check: " + err.Error(), Err: err, Hint: Hint(KindRetryable)}
	}
	now := c.cfg.Now()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e, _ := classifyStatus(resp.StatusCode, resp.Header, body, c.cfg.RetryAfterCap)
		e.Message = redactSecret(e.Message, key)
		c.status.keyCheck(now, e)
		return nil, e
	}
	if e := checkContentType(resp.Header, body); e != nil {
		return nil, e
	}
	var out struct {
		Data *KeyInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Data == nil {
		return nil, &Error{Kind: KindBadResponse, Status: resp.StatusCode, Message: "the key check returned no data object", Err: err, Hint: Hint(KindBadResponse)}
	}
	return out.Data, nil
}

// SetKeepWarm starts or stops the keep-warm loop. The gate turns it on while the policy has a
// synchronous (flag, block or fallback) route, so the first gated request finds a warm HTTP/2
// connection. The loop requests GET /api/v1/key on OpenRouter backends (free) and HEAD on the
// decision endpoint elsewhere, every TRUTHTABLE_JEV_KEEPALIVE.
func (c *httpClient) SetKeepWarm(active bool) {
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	if c.closed || c.cfg.KeepAlive <= 0 || active == c.warmOn {
		return
	}
	c.warmOn = active
	if active {
		c.warmStop, c.warmDone = make(chan struct{}), make(chan struct{})
		go c.keepWarm(c.warmStop, c.warmDone)
		return
	}
	close(c.warmStop)
	<-c.warmDone
}

// Close stops keep-warm for good and drops idle connections. Stopping and closing happen under one
// lock, so a concurrent SetKeepWarm(true) cannot restart the loop after Close.
func (c *httpClient) Close() error {
	c.warmMu.Lock()
	if c.warmOn {
		close(c.warmStop)
		<-c.warmDone
		c.warmOn = false
	}
	c.closed = true
	c.warmMu.Unlock()
	c.hc.CloseIdleConnections()
	return nil
}

func (c *httpClient) keepWarm(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(c.cfg.KeepAlive)
	defer t.Stop()
	for {
		c.ping(stop)
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// ping sends one keep-warm request, abandoned when stop closes.
func (c *httpClient) ping(stop <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), keepWarmTimeout)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	if c.ep.keyURL != "" {
		if _, err := c.CheckKey(ctx); err != nil && ctx.Err() == nil {
			c.log.Debug("jev keep-warm", "kind", ErrorKind(err), "err", err)
		}
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.ep.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", c.ua)
	if resp, err := c.hc.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
}
