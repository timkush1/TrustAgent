package llmapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// AnnotationPrefix starts the SSE comment line that carries the decision of a streamed answer:
// ": trustagent {...}" (SPEC §2.12, stream_annotations: comment).
const AnnotationPrefix = "trustagent "

// KeepaliveComment is the comment a buffered stream sends while the gateway reads the upstream
// answer; the first one commits the response headers (SPEC §2.12 buffer streaming).
const KeepaliveComment = "keepalive"

// Annotation is the decision summary a stream carries in its ": trustagent {...}" comment line,
// written just before the terminal event. The member order is the spec's.
type Annotation struct {
	TraceID     string   `json:"trace_id"`
	Action      string   `json:"action"`
	WouldAction string   `json:"would_action"`
	Status      string   `json:"status"`
	Verdict     *string  `json:"verdict"`
	MaxRisk     *float64 `json:"max_risk"`
	Reasons     []string `json:"reasons"`
}

// Comment returns the text of the annotation comment line (without the leading ": ").
func (a *Annotation) Comment() string {
	v := *a
	if v.Reasons == nil {
		v.Reasons = []string{}
	}
	return AnnotationPrefix + string(marshal(v))
}

// ErrStreamClosed is returned for a write after Finish.
var ErrStreamClosed = errors.New("llmapi: stream already finished")

// StreamWriter writes a server-sent event stream to a client: events are forwarded as they come
// (flushed one by one), the terminal event can be held back until the decision is known, and the
// decision goes out as a ": trustagent {...}" comment just before the terminal event and as HTTP
// trailers after the body.
//
// Browsers and nginx drop trailers and SDKs drop comments; clients that need the decision of a
// stream call GET /v1/trustagent/decisions/:trace_id (SPEC §2.12).
//
// An Incomplete event (the unterminated end of an upstream stream) is never followed by anything:
// its open last line would absorb the next bytes (the annotation comment would turn into the tail
// of a data line, which the client then dispatches as a malformed event). WriteEvent keeps it
// back, and Finish, or WriteEvents with an annotation, writes it last, where a client drops it as
// it would have without the gateway.
//
// The first write error is sticky: from then on every write returns it (the client went away).
// A StreamWriter is not safe for concurrent use.
type StreamWriter struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	trailers []string
	status   int
	wrote    bool
	held     *Event
	tail     []byte // the bytes of Incomplete events, written last
	finished bool
	err      error
}

// NewStreamWriter returns a StreamWriter on w. Headers already set on w (copied from the upstream
// response with multi-value Add) are sent with the first write.
func NewStreamWriter(w http.ResponseWriter) *StreamWriter {
	return &StreamWriter{w: w, rc: http.NewResponseController(w), status: http.StatusOK}
}

// DeclareTrailers announces trailer names in the Trailer header. Call it before the first write:
// HTTP/1.1 only sends trailers that were declared up front.
func (s *StreamWriter) DeclareTrailers(names ...string) {
	if s.wrote {
		return
	}
	for _, n := range names {
		n = http.CanonicalHeaderKey(n)
		if n != "" && !containsFold(s.trailers, n) {
			s.trailers = append(s.trailers, n)
		}
	}
}

// SetStatus sets the status the first write sends (200 by default).
func (s *StreamWriter) SetStatus(code int) {
	if !s.wrote {
		s.status = code
	}
}

// Committed reports whether the headers were sent: from then on the decision reaches the client
// only through the comment and the trailers, and a block must use the message style.
func (s *StreamWriter) Committed() bool { return s.wrote }

// Err is the sticky write error, if any.
func (s *StreamWriter) Err() error { return s.err }

// commit sends the headers: an event stream is never cached or buffered by a proxy, and its body
// is rewritten, so Content-Length and Content-Encoding go.
func (s *StreamWriter) commit() {
	if s.wrote {
		return
	}
	s.wrote = true
	h := s.w.Header()
	h.Del("Content-Length")
	h.Del("Content-Encoding")
	if !strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream") {
		h.Set("Content-Type", "text/event-stream")
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	if h.Get("X-Accel-Buffering") == "" {
		h.Set("X-Accel-Buffering", "no")
	}
	if len(s.trailers) > 0 {
		h.Set("Trailer", strings.Join(s.trailers, ", "))
	}
	s.w.WriteHeader(s.status)
}

// Commit sends the headers now (without body bytes).
func (s *StreamWriter) Commit() error {
	if s.err != nil {
		return s.err
	}
	s.commit()
	return s.flush()
}

// write sends p and flushes it.
func (s *StreamWriter) write(p []byte) error {
	if s.err != nil {
		return s.err
	}
	if s.finished {
		return ErrStreamClosed
	}
	s.commit()
	if _, err := s.w.Write(p); err != nil {
		s.err = err
		return err
	}
	return s.flush()
}

func (s *StreamWriter) flush() error {
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.err = err
		return err
	}
	return nil
}

// WriteEvent forwards one event: its Raw bytes when it was read, else its encoding. An Incomplete
// event is kept back and written last (see StreamWriter).
func (s *StreamWriter) WriteEvent(ev Event) error {
	if ev.Incomplete {
		if s.err != nil {
			return s.err
		}
		if s.finished {
			return ErrStreamClosed
		}
		s.tail = append(s.tail, ev.Bytes()...)
		return nil
	}
	return s.write(ev.Bytes())
}

// WriteEvents writes a collected or built stream (a buffered answer, a block or a synthesized
// stream), putting the annotation comment, when given, just before the terminal event of format,
// or at the end when the stream has none. With an annotation the stream is complete, so an
// Incomplete tail goes out after it; without one the tail waits for Finish.
func (s *StreamWriter) WriteEvents(format string, events []Event, ann *Annotation) error {
	annotated := ann == nil
	for i := range events {
		if !annotated && IsTerminal(format, events[i]) {
			if err := s.WriteAnnotation(*ann); err != nil {
				return err
			}
			annotated = true
		}
		if err := s.WriteEvent(events[i]); err != nil {
			return err
		}
	}
	if ann == nil {
		return nil
	}
	if !annotated {
		if err := s.WriteAnnotation(*ann); err != nil {
			return err
		}
	}
	return s.writeTail()
}

// writeTail writes the Incomplete events kept back, if any.
func (s *StreamWriter) writeTail() error {
	if len(s.tail) == 0 {
		return nil
	}
	tail := s.tail
	s.tail = nil
	return s.write(tail)
}

// WriteComment writes a comment event, one ": " line per line of text.
func (s *StreamWriter) WriteComment(text string) error {
	ev := CommentEvent(text)
	return s.write(ev.Encode())
}

// WriteKeepalive writes the keepalive comment of a buffered stream (the first one commits the
// headers).
func (s *StreamWriter) WriteKeepalive() error { return s.WriteComment(KeepaliveComment) }

// WriteAnnotation writes the ": trustagent {...}" comment line.
func (s *StreamWriter) WriteAnnotation(a Annotation) error { return s.WriteComment(a.Comment()) }

// Hold keeps ev (the terminal event) back instead of writing it; Finish releases it. Holding
// again replaces the held event.
func (s *StreamWriter) Hold(ev Event) { s.held = &ev }

// Holding reports whether an event is held back.
func (s *StreamWriter) Holding() bool { return s.held != nil }

// Release writes the held event, if any.
func (s *StreamWriter) Release() error {
	if s.held == nil {
		return nil
	}
	ev := *s.held
	s.held = nil
	return s.WriteEvent(ev)
}

// Finish ends the stream: the annotation comment (when ann is not nil), then the held terminal
// event, then an Incomplete tail, then the trailers. A trailer name that was declared is set as
// is; any other is sent with the net/http trailer prefix (HTTP/2 and chunked HTTP/1.1 deliver
// both). Trailers are set even when a write fails, and Finish reports the first error.
func (s *StreamWriter) Finish(ann *Annotation, trailers map[string]string) error {
	var err error
	if ann != nil {
		err = s.WriteAnnotation(*ann)
	}
	if err == nil {
		err = s.Release()
	}
	if err == nil {
		err = s.writeTail()
	}
	if err == nil {
		err = s.Commit()
	}
	s.SetTrailers(trailers)
	s.finished = true
	return err
}

// SetTrailers sets trailer values (see Finish); call it after the last body write.
func (s *StreamWriter) SetTrailers(trailers map[string]string) {
	h := s.w.Header()
	for k, v := range trailers {
		k = http.CanonicalHeaderKey(k)
		if containsFold(s.trailers, k) {
			h.Set(k, v)
		} else {
			h.Set(http.TrailerPrefix+k, v)
		}
	}
}

// ExtendWriteDeadline pushes the connection's write deadline to now+d (before a fallback call,
// SPEC §2.12). It returns http.ErrNotSupported when the ResponseWriter cannot do it.
func (s *StreamWriter) ExtendWriteDeadline(d time.Duration) error {
	return s.rc.SetWriteDeadline(time.Now().Add(d))
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
