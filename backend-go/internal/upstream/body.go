package upstream

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/metrics"
)

// ErrUndecodable is the read error of a body whose Content-Encoding the gateway cannot decode, or
// whose encoded bytes are broken (B2): the answer cannot be gated (skipped:undecodable).
var ErrUndecodable = errors.New("upstream: the response body cannot be decoded")

// timeoutError is an upstream exchange that ran out of time: the total timeout of a JSON answer
// or the idle timeout between the reads of a stream. It is a net.Error whose Timeout is true and
// it matches context.DeadlineExceeded, so IsTimeout reports it (504, not 502).
type timeoutError struct{ msg string }

func (e *timeoutError) Error() string        { return e.msg }
func (e *timeoutError) Timeout() bool        { return true }
func (e *timeoutError) Temporary() bool      { return false }
func (e *timeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// The timeouts an exchange ends with.
var (
	errTotalTimeout = &timeoutError{"upstream: the answer took longer than TRUTHTABLE_UPSTREAM_TIMEOUT"}
	errIdleTimeout  = &timeoutError{"upstream: the stream was silent for longer than TRUTHTABLE_STREAM_IDLE_TIMEOUT"}
)

// deadline aborts one upstream exchange when its timer runs out: the total timeout while Do waits
// for the answer and its JSON body is read, then, once StreamBody takes the body over, the idle
// timeout of each read. Firing cancels the request's context with the timeout as the cause, which
// the transport reports to a pending read; the reads then fail with the timeout itself.
type deadline struct {
	cancel context.CancelCauseFunc

	mu    sync.Mutex
	timer *time.Timer
	armed bool
	at    time.Time
	cause error
	fired error // the timeout that aborted the exchange
}

// arm (re)starts the timer: after d the exchange is aborted with cause. d <= 0 disarms it.
func (d *deadline) arm(after time.Duration, cause error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fired != nil {
		return
	}
	if after <= 0 {
		d.armed = false
		if d.timer != nil {
			d.timer.Stop()
		}
		return
	}
	d.armed, d.at, d.cause = true, time.Now().Add(after), cause
	if d.timer == nil {
		d.timer = time.AfterFunc(after, d.expire)
		return
	}
	d.timer.Reset(after)
}

// disarm stops the timer.
func (d *deadline) disarm() { d.arm(0, nil) }

// expire is the timer's callback. A callback that was already running when the timer was
// re-armed or stopped finds the timer disarmed or its deadline in the future and does nothing.
func (d *deadline) expire() {
	d.mu.Lock()
	if !d.armed || d.fired != nil || time.Now().Before(d.at) {
		d.mu.Unlock()
		return
	}
	d.fired, d.armed = d.cause, false
	cause := d.cause
	d.mu.Unlock()
	d.cancel(cause)
}

// err is the timeout that aborted the exchange, or nil.
func (d *deadline) err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

// timedBody is the body of a response Do returned: its reads run under the exchange's deadline
// (the total timeout, or per read the idle timeout once StreamBody switched it to a stream), a
// read the deadline aborted fails with the timeout, and the end of the body (EOF, a read error or
// Close, whichever comes first) is observed as the upstream answer time (upstream_seconds).
type timedBody struct {
	rc     io.ReadCloser
	d      *deadline
	start  time.Time
	format string // the upstream_seconds label; "" observes nothing
	idle   time.Duration

	endOnce   sync.Once
	closeOnce sync.Once
	closeErr  error
}

func (b *timedBody) Read(p []byte) (int, error) {
	if b.idle > 0 {
		b.d.arm(b.idle, errIdleTimeout)
	}
	n, err := b.rc.Read(p)
	if b.idle > 0 {
		b.d.disarm()
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			b.d.disarm() // a whole JSON body: the total timeout is done with
		} else if fired := b.d.err(); fired != nil {
			err = fired
		}
		b.end()
	}
	return n, err
}

func (b *timedBody) Close() error {
	b.closeOnce.Do(func() {
		b.end()
		b.closeErr = b.rc.Close()
	})
	return b.closeErr
}

// end observes the upstream answer time once.
func (b *timedBody) end() {
	b.endOnce.Do(func() {
		if b.format != "" && !b.start.IsZero() {
			metrics.ObserveUpstream(b.format, time.Since(b.start))
		}
	})
}

// timedOf finds the timedBody under a body DecodeBody may have wrapped, or nil.
func timedOf(rc io.ReadCloser) *timedBody {
	switch b := rc.(type) {
	case *timedBody:
		return b
	case *decodedBody:
		if t, ok := b.src.(*timedBody); ok {
			return t
		}
	}
	return nil
}

// StartedAt is when Do sent the request resp answers; zero when resp did not come from Do. The
// upstream time and the time to the first streamed event of a trace count from it.
func StartedAt(resp *http.Response) time.Time {
	if resp == nil {
		return time.Time{}
	}
	if t := timedOf(resp.Body); t != nil {
		return t.start
	}
	return time.Time{}
}

// StreamBody is the body of a streamed (text/event-stream) response for the relay, decoded as
// DecodeBody does. From here on the total timeout (TRUTHTABLE_UPSTREAM_TIMEOUT) no longer applies:
// a long answer streams for as long as the upstream keeps sending, and each read must return
// within the idle timeout (TRUTHTABLE_STREAM_IDLE_TIMEOUT), else the request is cancelled and the
// read fails with a timeout error (IsTimeout). A stalled stream is cut off; a slow, steady one is
// not. Closing the returned body closes resp.Body and calls cancel (Do's), once.
func (c *Client) StreamBody(resp *http.Response, cancel context.CancelFunc) io.ReadCloser {
	tb := timedOf(resp.Body)
	if tb == nil {
		// A response Do did not make: the idle timer cancels it through cancel and closes the
		// body, which ends a read that is still waiting.
		src := resp.Body
		abort := func(error) {
			if cancel != nil {
				cancel()
			}
			_ = src.Close()
		}
		tb = &timedBody{rc: src, d: &deadline{cancel: abort}}
		resp.Body = tb
	}
	tb.d.disarm()
	tb.idle = c.idle
	return &streamBody{ReadCloser: DecodeBody(resp), cancel: cancel}
}

// streamBody closes the relayed body and releases the exchange, once.
type streamBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

func (s *streamBody) Close() error {
	s.once.Do(func() {
		s.err = s.ReadCloser.Close()
		if s.cancel != nil {
			s.cancel()
		}
	})
	return s.err
}

// DecodeBody returns resp.Body with a stray Content-Encoding decoded (B2): the gateway never
// forwards the client's Accept-Encoding, so the transport negotiates gzip itself and decodes it
// transparently, and an encoding that still arrives is decoded here: gzip, x-gzip and deflate
// (zlib, or raw deflate). Any other encoding, or broken encoded bytes, fails the reads with
// ErrUndecodable. The decoder starts on the first read, so DecodeBody never blocks. resp is
// updated in place (Body, the encoding headers, ContentLength and Uncompressed), so a second call
// returns the same body.
func DecodeBody(resp *http.Response) io.ReadCloser {
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if enc == "identity" {
		resp.Header.Del("Content-Encoding")
		enc = ""
	}
	if enc == "" || resp.Uncompressed {
		return resp.Body
	}
	d := &decodedBody{src: resp.Body}
	switch enc {
	case "gzip", "x-gzip":
		d.open = func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }
	case "deflate":
		d.open = openDeflate
	default:
		d.err = fmt.Errorf("%w: Content-Encoding %q", ErrUndecodable, enc)
	}
	resp.Body = d
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return d
}

// openDeflate reads HTTP deflate: a zlib stream as RFC 9110 defines it, or the raw deflate some
// servers send instead.
func openDeflate(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(2)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// decodedBody decodes an encoded body lazily.
type decodedBody struct {
	src  io.ReadCloser
	open func(io.Reader) (io.Reader, error)
	r    io.Reader
	err  error // sticky
}

func (d *decodedBody) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	if d.r == nil {
		r, err := d.open(d.src)
		if err != nil {
			d.err = undecodable(err)
			return 0, d.err
		}
		d.r = r
	}
	n, err := d.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = undecodable(err)
		d.err = err
	}
	return n, err
}

// Close closes the source. The decoders hold nothing to release, and leaving them alone keeps a
// Close that races a read (a relay that drains the end of a stream) free of data races.
func (d *decodedBody) Close() error { return d.src.Close() }

// undecodable marks the errors of broken encoded bytes as ErrUndecodable. The source's own errors
// (a timeout, a reset connection, a body cut short) are kept as they are.
func undecodable(err error) error {
	var corrupt flate.CorruptInputError
	if errors.Is(err, gzip.ErrHeader) || errors.Is(err, gzip.ErrChecksum) || errors.Is(err, zlib.ErrHeader) ||
		errors.Is(err, zlib.ErrChecksum) || errors.Is(err, zlib.ErrDictionary) || errors.As(err, &corrupt) {
		return fmt.Errorf("%w: %v", ErrUndecodable, err)
	}
	return err
}

// ReadBody reads at most limit bytes of a response body, decoded as DecodeBody does. It reports
// whether the body is larger; the caller then streams the rest from resp.Body after what was read.
// A body that cannot be decoded fails with ErrUndecodable.
func ReadBody(resp *http.Response, limit int64) ([]byte, bool, error) {
	body := DecodeBody(resp)
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b, true, nil
	}
	return b, false, nil
}
