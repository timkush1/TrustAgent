package llmapi

import (
	"errors"
	"io"
	"time"
)

// RelayResult reports how a passthrough relay ended.
type RelayResult struct {
	// Held is set when the terminal event arrived: the writer holds it until Finish.
	Held bool
	// Events and Bytes count what was read from the upstream.
	Events int
	Bytes  int64
	// ClientErr is the write error that ended the relay: the client went away. The answer is
	// then gated unenforced and audited with the client_disconnected flag when the assembler saw
	// the finish reason (Finished), and skipped otherwise (SPEC §2.12).
	ClientErr error
	// UpstreamErr is the read error that ended the relay, other than a clean end of stream
	// (ErrEventTooLarge included).
	UpstreamErr error
}

// Relay is the passthrough streaming loop: it reads the upstream stream event by event, feeds
// each event to asm and forwards it to the client at once, except the terminal event, which it
// hands to dst.Hold and returns on. The caller then waits for the decision (or the deadline) and
// calls dst.Finish with the annotation and the trailers.
//
// Relay returns at the terminal event, at the end of the upstream stream (Held false: there is
// nothing to hold, Finish still writes the annotation), on a read error, or on the first failed
// write.
func Relay(src *Reader, asm *StreamAssembler, dst *StreamWriter) RelayResult {
	var res RelayResult
	for {
		ev, err := src.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				res.UpstreamErr = err
			}
			return res
		}
		res.Events++
		res.Bytes += int64(len(ev.Raw))
		if asm.Add(ev) == EventTerminal {
			dst.Hold(ev)
			res.Held = true
			return res
		}
		if err := dst.WriteEvent(ev); err != nil {
			res.ClientErr = err
			return res
		}
	}
}

// CollectOptions configure Collect.
type CollectOptions struct {
	// MaxBytes stops collecting once the events read exceed it (0: no limit); the caller then
	// relays the rest ungated (skipped:upstream_error).
	MaxBytes int64
	// Keepalive, when positive, writes a keepalive comment to Writer at that interval while the
	// upstream answer is read (route buffer_keepalive_s). The first one commits the headers.
	Keepalive time.Duration
	Writer    *StreamWriter
}

// CollectResult is what Collect read.
type CollectResult struct {
	// Events are the events read, the terminal event included; a buffered answer that is
	// delivered as is goes out with StreamWriter.WriteEvents, which puts the annotation before
	// the terminal event.
	Events []Event
	Bytes  int64
	// Terminated is set when the terminal event was read.
	Terminated bool
	// TooLarge is set when MaxBytes was exceeded; Events holds what was read so far and the
	// Reader can go on from there.
	TooLarge bool
	// Keepalives counts the keepalive comments written.
	Keepalives int
	// ClientErr is the keepalive write error that ended collection (the client went away). A
	// read may still be in flight on the Reader: close the upstream body and do not use the
	// Reader again.
	ClientErr error
	// UpstreamErr is the read error that ended collection, other than a clean end of stream.
	UpstreamErr error
}

// Collect is the buffer streaming read: it reads the whole upstream stream (up to its terminal
// event) into memory, feeding asm, so the gate can decide before the client sees any of it.
func Collect(src *Reader, asm *StreamAssembler, opts CollectOptions) CollectResult {
	var res CollectResult
	// add records one read; true means stop.
	add := func(ev Event, err error) bool {
		if err != nil {
			if !errors.Is(err, io.EOF) {
				res.UpstreamErr = err
			}
			return true
		}
		res.Events = append(res.Events, ev)
		res.Bytes += int64(len(ev.Raw))
		if asm.Add(ev) == EventTerminal {
			res.Terminated = true
			return true
		}
		if opts.MaxBytes > 0 && res.Bytes > opts.MaxBytes {
			res.TooLarge = true
			return true
		}
		return false
	}
	if opts.Keepalive <= 0 || opts.Writer == nil {
		for {
			if add(src.Next()) {
				return res
			}
		}
	}

	// The reads run in a goroutine so keepalives go out while the upstream is silent. The
	// goroutine only reads the next event when asked, so once Collect returns for any reason
	// other than a failed keepalive, no read is in flight.
	type item struct {
		ev  Event
		err error
	}
	items := make(chan item)
	next := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			ev, err := src.Next()
			select {
			case items <- item{ev, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
			select {
			case <-next:
			case <-done:
				return
			}
		}
	}()
	ticker := time.NewTicker(opts.Keepalive)
	defer ticker.Stop()
	for {
		select {
		case it := <-items:
			if add(it.ev, it.err) {
				return res
			}
			next <- struct{}{}
		case <-ticker.C:
			if err := opts.Writer.WriteKeepalive(); err != nil {
				res.ClientErr = err
				return res
			}
			res.Keepalives++
		}
	}
}
