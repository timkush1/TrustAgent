// Package websocket is the dashboard's live stream: WS protocol v2 (SPEC §2.5), with v1 frames
// for clients that do not negotiate v2.
//
// Hub.Run alone owns the client map, the replay ring and the sequence counter (B6): producers
// hand events to Run through one buffered channel, and connections talk to it through register
// and unregister. Every event is one JSON document in one text frame (B5). Events carry an epoch
// (per hub instance) and a seq; a reconnecting client passes both back and gets the events it
// missed from the replay ring, or a gap telling it to resync over REST.
//
// Callers authenticate before ServeWS (sessions, WS tickets and cookie auth arrive with the P2
// authz core) and say whether the caller may see content; clients without the content scope get
// the redacted variant of every event.
package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/truthtable/backend-go/internal/metrics"
)

// Protocol versions.
const (
	ProtocolV1 = 1
	ProtocolV2 = 2
)

// Close codes (SPEC §2.5).
const (
	CloseUnauthorized = 4401
	CloseTooMany      = 4429
)

// Gap reasons.
const (
	GapSlowConsumer  = "slow_consumer"
	GapBufferEvicted = "buffer_evicted"
	GapEpochChanged  = "epoch_changed"
	GapHubOverflow   = "hub_overflow"
)

const (
	writeWait     = 10 * time.Second
	sweepInterval = time.Second
	// resyncOverlap is how far before the first missed event a gap's resync URLs start.
	resyncOverlap = 5 * time.Second
	resyncLimit   = 200
	readLimit     = 4096
)

// Event is one event to broadcast. Data is the JSON of the event's data object. Redacted is the
// variant for clients without the content scope (nil: Data). Ring is the variant the replay ring
// keeps (nil: Data); audit_completed keeps its audit without context_used there. V1Type and V1
// make the frame v1 clients get (nil V1: v1 clients do not get this event).
type Event struct {
	Type     string
	TraceID  string
	Data     json.RawMessage
	Redacted json.RawMessage
	Ring     json.RawMessage
	V1Type   string
	V1       json.RawMessage
}

// NewEvent marshals the full and the redacted data of an event; a nil redacted means the data
// carries no content.
func NewEvent(typ, traceID string, data, redacted any) (Event, error) {
	ev := Event{Type: typ, TraceID: traceID}
	var err error
	if ev.Data, err = json.Marshal(data); err != nil {
		return Event{}, err
	}
	if redacted != nil {
		if ev.Redacted, err = json.Marshal(redacted); err != nil {
			return Event{}, err
		}
	}
	return ev, nil
}

// HelloInfo is the part of the hello event the hub does not know itself.
type HelloInfo struct {
	Demo     bool
	Readonly bool
	// Status is the SystemStatusLite object.
	Status any
}

// Config configures a Hub. Zero values take the SPEC defaults.
type Config struct {
	// Version is the server_version of hello.
	Version string
	// Epoch identifies this hub instance; empty generates a UUIDv7.
	Epoch string
	// ReplayEvents and ReplayMaxBytes cap the replay ring (TRUTHTABLE_WS_REPLAY_BUFFER, 2000, and
	// _WS_REPLAY_MAX_BYTES, 64 MiB). A negative ReplayEvents disables replay.
	ReplayEvents   int
	ReplayMaxBytes int64
	MaxClients     int // TRUTHTABLE_WS_MAX_CLIENTS (200)
	MaxPerIP       int // TRUTHTABLE_WS_MAX_PER_IP (10)
	// Heartbeat is the hb interval (TRUTHTABLE_WS_HEARTBEAT, 20s).
	Heartbeat time.Duration
	// WriteStall closes a client with no successful write for this long (30s).
	WriteStall time.Duration
	// ClientBuffer is the per-client frame buffer (256); ProducerBuffer the producer channel (4096).
	ClientBuffer   int
	ProducerBuffer int
	// AllowedOrigins are the browser origins allowed to connect; an empty Origin is allowed.
	AllowedOrigins []string
	// Hello supplies demo, readonly and the status summary of hello; nil sends zero values.
	Hello  func() HelloInfo
	Now    func() time.Time
	Logger *slog.Logger
}

func (c *Config) defaults() {
	if c.Epoch == "" {
		c.Epoch = uuid.Must(uuid.NewV7()).String()
	}
	if c.ReplayEvents == 0 {
		c.ReplayEvents = 2000
	}
	if c.ReplayMaxBytes <= 0 {
		c.ReplayMaxBytes = 64 << 20
	}
	if c.MaxClients <= 0 {
		c.MaxClients = 200
	}
	if c.MaxPerIP <= 0 {
		c.MaxPerIP = 10
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 20 * time.Second
	}
	if c.WriteStall <= 0 {
		c.WriteStall = 30 * time.Second
	}
	if c.ClientBuffer <= 0 {
		c.ClientBuffer = 256
	}
	if c.ProducerBuffer <= 0 {
		c.ProducerBuffer = 4096
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Hello == nil {
		c.Hello = func() HelloInfo { return HelloInfo{} }
	}
}

// Hub is the broadcast hub. Create it with NewHub and start Run.
type Hub struct {
	cfg      Config
	started  time.Time
	upgrader websocket.Upgrader

	events     chan Event
	register   chan *registration
	unregister chan *client
	done       chan struct{}
	overflow   atomic.Int64
	count      atomic.Int64

	// Owned by Run.
	clients map[*client]struct{}
	perIP   map[string]int
	seq     int64
	ring    *ring
}

// NewHub builds a hub.
func NewHub(cfg Config) *Hub {
	cfg.defaults()
	h := &Hub{
		cfg: cfg, started: cfg.Now().UTC(),
		events: make(chan Event, cfg.ProducerBuffer), register: make(chan *registration),
		unregister: make(chan *client), done: make(chan struct{}),
		clients: map[*client]struct{}{}, perIP: map[string]int{},
		ring: newRing(max(cfg.ReplayEvents, 0), cfg.ReplayMaxBytes),
	}
	origins := slices.Clone(cfg.AllowedOrigins)
	h.upgrader = websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 4096, CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || slices.Contains(origins, origin)
	}}
	return h
}

// Epoch returns the hub's epoch.
func (h *Hub) Epoch() string { return h.cfg.Epoch }

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int { return int(h.count.Load()) }

// Publish hands an event to Run without blocking. When the producer channel is full the event is
// dropped and counted; Run then sends every client a hub_overflow gap.
func (h *Hub) Publish(ev Event) {
	select {
	case h.events <- ev:
	default:
		h.overflow.Add(1)
		metrics.WSDropped.Inc()
	}
}

// Run owns the hub state until ctx ends, then closes every client.
func (h *Hub) Run(ctx context.Context) {
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()
	defer close(h.done)
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				h.remove(c)
			}
			return
		case r := <-h.register:
			r.reply <- h.add(r)
		case c := <-h.unregister:
			h.remove(c)
		case ev := <-h.events:
			h.flushOverflow()
			h.dispatch(ev)
		case <-sweep.C:
			h.flushOverflow()
			h.sweep()
		}
	}
}

// envelope is the v2 frame.
type envelope struct {
	V       int             `json:"v"`
	Seq     *int64          `json:"seq,omitempty"`
	Epoch   string          `json:"epoch,omitempty"`
	Type    string          `json:"type"`
	TS      string          `json:"ts"`
	TraceID string          `json:"trace_id,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// v1Frame is the frame of protocol v1.
type v1Frame struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func (h *Hub) frame(seq int64, typ, traceID string, ts time.Time, data json.RawMessage) []byte {
	b, err := json.Marshal(envelope{V: ProtocolV2, Seq: &seq, Epoch: h.cfg.Epoch, Type: typ, TS: formatTS(ts), TraceID: traceID, Data: data})
	if err != nil {
		h.cfg.Logger.Error("ws: event does not marshal", "type", typ, "err", err)
		return nil
	}
	return b
}

// dispatch assigns the next seq, keeps the event in the ring and delivers it.
func (h *Hub) dispatch(ev Event) {
	if len(ev.Data) == 0 {
		ev.Data = json.RawMessage("{}")
	}
	now := h.cfg.Now().UTC()
	seq := h.seq + 1
	full := h.frame(seq, ev.Type, ev.TraceID, now, ev.Data)
	if full == nil {
		return
	}
	redacted := full
	if ev.Redacted != nil {
		if redacted = h.frame(seq, ev.Type, ev.TraceID, now, ev.Redacted); redacted == nil {
			return
		}
	}
	// The ring keeps the ring variant (audit_completed without context_used); an event without a
	// redacted variant carries no content, so its ring variant serves every client.
	ringFull, ringRedacted := full, redacted
	if ev.Ring != nil {
		if ringFull = h.frame(seq, ev.Type, ev.TraceID, now, ev.Ring); ringFull == nil {
			return
		}
		if ev.Redacted == nil {
			ringRedacted = ringFull
		}
	}
	var v1 []byte
	if ev.V1 != nil {
		v1, _ = json.Marshal(v1Frame{Type: ev.V1Type, Timestamp: now.Format(time.RFC3339), Data: ev.V1})
	}
	h.seq = seq
	size := int64(len(ringFull))
	if ev.Redacted != nil {
		size += int64(len(ringRedacted))
	}
	h.ring.push(ringEntry{seq: seq, ts: now, full: ringFull, redacted: ringRedacted, size: size})
	for c := range h.clients {
		h.deliver(c, seq, now, c.pick(full, redacted, v1))
	}
}

// deliver queues frame for c without blocking. A client whose buffer is full misses the event
// and gets a slow_consumer gap covering everything it missed before its next delivered event.
func (h *Hub) deliver(c *client, seq int64, ts time.Time, frame []byte) {
	if frame == nil {
		return
	}
	if c.gapFrom > 0 && !h.flushGap(c) {
		c.gapTo = seq
		metrics.WSDropped.Inc()
		return
	}
	select {
	case c.send <- frame:
	default:
		metrics.WSDropped.Inc()
		if c.protocol != ProtocolV2 {
			return
		}
		c.gapFrom, c.gapTo, c.gapTS = seq, seq, ts
	}
}

// flushGap queues c's pending gap when there is room for it and one more frame.
func (h *Hub) flushGap(c *client) bool {
	if cap(c.send)-len(c.send) < 2 {
		return false
	}
	c.send <- h.gapFrame(c.gapFrom, c.gapTo, c.gapReason(), c.gapTS)
	c.gapFrom, c.gapTo, c.overflowGap = 0, 0, false
	return true
}

func (h *Hub) gapFrame(from, to int64, reason string, since time.Time) []byte {
	type resync struct {
		Traces    string `json:"traces"`
		Decisions string `json:"decisions"`
		Audits    string `json:"audits"`
	}
	type gap struct {
		FromSeq int64  `json:"from_seq"`
		ToSeq   int64  `json:"to_seq"`
		Reason  string `json:"reason"`
		Resync  resync `json:"resync"`
	}
	ts := url.QueryEscape(formatTS(since.Add(-resyncOverlap)))
	limit := "&limit=" + strconv.Itoa(resyncLimit)
	data, _ := json.Marshal(gap{FromSeq: from, ToSeq: to, Reason: reason, Resync: resync{
		Traces:    "/api/traces?updated_since=" + ts + limit,
		Decisions: "/api/gate/decisions?updated_since=" + ts + limit,
		Audits:    "/api/audits?completed_since=" + ts + limit,
	}})
	metrics.WSGaps.WithLabelValues(reason).Inc()
	// A gap carries the seq of the last event it covers, so the next event follows it.
	return h.frame(to, "gap", "", h.cfg.Now().UTC(), data)
}

// flushOverflow turns events dropped at Publish into a hub_overflow gap for every client. The
// dropped events get the seqs the gap covers, so replays across them are detected as well.
func (h *Hub) flushOverflow() {
	n := h.overflow.Swap(0)
	if n == 0 {
		return
	}
	from, now := h.seq+1, h.cfg.Now().UTC()
	h.seq += n
	h.cfg.Logger.Warn("ws: the event channel overflowed; clients will resync", "dropped", n)
	for c := range h.clients {
		if c.protocol != ProtocolV2 {
			continue
		}
		if c.gapFrom == 0 {
			c.gapFrom, c.gapTS, c.overflowGap = from, now, true
		}
		c.gapTo = h.seq
		h.flushGap(c)
	}
}

// sweep closes clients with no successful write for WriteStall and sends pending gaps.
func (h *Hub) sweep() {
	now := h.cfg.Now()
	for c := range h.clients {
		if now.Sub(time.Unix(0, c.lastWrite.Load())) > h.cfg.WriteStall {
			h.cfg.Logger.Info("ws: closing a client with no successful write", "stall", h.cfg.WriteStall)
			_ = c.conn.Close()
			h.remove(c)
			continue
		}
		if c.gapFrom > 0 {
			h.flushGap(c)
		}
	}
}

// registration is a connecting client and its replay request.
type registration struct {
	c        *client
	sinceSeq *int64
	epoch    string
	reply    chan error
}

var errTooMany = errors.New("ws: too many connections")

// add registers a client: caps, hello, then the replay or a gap.
func (h *Hub) add(r *registration) error {
	c := r.c
	if len(h.clients) >= h.cfg.MaxClients || h.perIP[c.ip] >= h.cfg.MaxPerIP {
		return errTooMany
	}
	h.clients[c] = struct{}{}
	h.perIP[c.ip]++
	h.count.Store(int64(len(h.clients)))
	metrics.WSClients.Set(float64(len(h.clients)))
	c.lastWrite.Store(h.cfg.Now().UnixNano())
	if c.protocol != ProtocolV2 {
		return nil
	}

	var (
		replay       []ringEntry
		replayedFrom *int64
		gap          []byte
	)
	if since := r.sinceSeq; since != nil {
		switch {
		case r.epoch != h.cfg.Epoch:
			gap = h.gapFrame(0, h.seq, GapEpochChanged, h.started)
		case *since >= h.seq:
			// Up to date: nothing to replay.
		case h.ring.has(*since + 1):
			replay = h.ring.after(*since)
			from := *since + 1
			replayedFrom = &from
		default:
			gap = h.gapFrame(*since+1, h.seq, GapBufferEvicted, h.ring.oldestTS(h.started))
		}
	}
	if need := len(replay) + 2; need > cap(c.send)-len(c.send) {
		// The write pump has not started yet, so the buffer can still grow: a replaying client
		// gets room for hello, a gap and its replay on top of the normal buffer, rather than a
		// slow_consumer gap for events it asked for.
		c.send = make(chan []byte, h.cfg.ClientBuffer+need)
	}
	info := h.cfg.Hello()
	hello, err := json.Marshal(struct {
		Protocol      int    `json:"protocol"`
		ServerVersion string `json:"server_version"`
		Epoch         string `json:"epoch"`
		LastSeq       int64  `json:"last_seq"`
		ReplayedFrom  *int64 `json:"replayed_from"`
		Demo          bool   `json:"demo"`
		Readonly      bool   `json:"readonly"`
		Status        any    `json:"status"`
	}{ProtocolV2, h.cfg.Version, h.cfg.Epoch, h.seq, replayedFrom, info.Demo, info.Readonly, info.Status})
	if err != nil {
		h.cfg.Logger.Error("ws: hello does not marshal", "err", err)
		hello = nil
	}
	c.send <- h.frame(h.seq, "hello", "", h.cfg.Now().UTC(), hello)
	if gap != nil {
		c.send <- gap
	}
	for _, e := range replay {
		h.deliver(c, e.seq, e.ts, c.pick(e.full, e.redacted, nil))
	}
	return nil
}

// remove unregisters c and closes its send channel, which ends its write pump.
func (h *Hub) remove(c *client) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	if h.perIP[c.ip]--; h.perIP[c.ip] <= 0 {
		delete(h.perIP, c.ip)
	}
	close(c.send)
	h.count.Store(int64(len(h.clients)))
	metrics.WSClients.Set(float64(len(h.clients)))
}

// ConnOptions describe an authenticated connection.
type ConnOptions struct {
	// Content says whether the caller holds the content scope (full events); otherwise it gets
	// the redacted variants.
	Content bool
	// IP keys the per-IP cap.
	IP string
}

// ServeWS upgrades an authenticated request and serves it until either side closes. The query
// selects the protocol (protocol=2; anything else is v1) and, for v2, the replay position
// (since_seq and epoch). Over the connection caps the socket closes with 4429.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, opts ConnOptions) {
	q := r.URL.Query()
	protocol := ProtocolV1
	if q.Get("protocol") == "2" {
		protocol = ProtocolV2
	}
	var since *int64
	if raw := q.Get("since_seq"); raw != "" && protocol == ProtocolV2 {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= 0 {
			since = &n
		}
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.cfg.Logger.Info("ws: upgrade failed", "err", err)
		return
	}
	c := &client{conn: conn, send: make(chan []byte, h.cfg.ClientBuffer), protocol: protocol, content: opts.Content, ip: opts.IP}
	reg := &registration{c: c, sinceSeq: since, epoch: q.Get("epoch"), reply: make(chan error, 1)}
	select {
	case h.register <- reg:
		err = <-reg.reply
	case <-h.done:
		err = errors.New("ws: hub stopped")
	}
	if err != nil {
		code, text := websocket.CloseGoingAway, "server shutting down"
		if errors.Is(err, errTooMany) {
			code, text = CloseTooMany, "too many connections"
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(writeWait))
		_ = conn.Close()
		return
	}
	go c.writePump(h)
	c.readPump(h)
}

// client is one connection. The gap fields are owned by Run.
type client struct {
	conn     *websocket.Conn
	send     chan []byte
	protocol int
	content  bool
	ip       string
	// lastWrite is the unix nanos of the last successful write (the pump stores, Run reads).
	lastWrite atomic.Int64

	gapFrom, gapTo int64
	gapTS          time.Time
	overflowGap    bool
}

func (c *client) gapReason() string {
	if c.overflowGap {
		return GapHubOverflow
	}
	return GapSlowConsumer
}

// pick returns the frame variant for c.
func (c *client) pick(full, redacted, v1 []byte) []byte {
	switch {
	case c.protocol != ProtocolV2:
		if !c.content {
			return nil
		}
		return v1
	case c.content:
		return full
	default:
		return redacted
	}
}

// writePump writes queued frames, one message per frame (B5), and a heartbeat every interval: an
// hb frame for v2 clients and a ping for everyone.
func (c *client) writePump(h *Hub) {
	ticker := time.NewTicker(h.cfg.Heartbeat)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case frame, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
			c.lastWrite.Store(h.cfg.Now().UnixNano())
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if c.protocol == ProtocolV2 {
				hb, _ := json.Marshal(envelope{V: ProtocolV2, Type: "hb", TS: formatTS(h.cfg.Now())})
				if err := c.conn.WriteMessage(websocket.TextMessage, hb); err != nil {
					return
				}
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			c.lastWrite.Store(h.cfg.Now().UnixNano())
		}
	}
}

// readPump discards what the client sends (never logging it) and keeps the read deadline alive
// on pongs. When the connection ends it unregisters the client.
func (c *client) readPump(h *Hub) {
	defer func() {
		select {
		case h.unregister <- c:
		case <-h.done:
		}
		_ = c.conn.Close()
	}()
	wait := 3 * h.cfg.Heartbeat
	c.conn.SetReadLimit(readLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(wait))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(wait)) })
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(wait))
	}
}

// ringEntry is one event as the replay ring keeps it.
type ringEntry struct {
	seq      int64
	ts       time.Time
	full     []byte
	redacted []byte
	size     int64
}

// ring keeps the newest events, capped by count and by bytes (SPEC §2.5).
type ring struct {
	buf      []ringEntry
	head     int // index of the oldest entry
	n        int
	bytes    int64
	maxBytes int64
}

func newRing(events int, maxBytes int64) *ring {
	return &ring{buf: make([]ringEntry, events), maxBytes: maxBytes}
}

func (r *ring) push(e ringEntry) {
	if len(r.buf) == 0 {
		return
	}
	if r.n == len(r.buf) {
		r.evict()
	}
	r.buf[(r.head+r.n)%len(r.buf)] = e
	r.n++
	r.bytes += e.size
	for r.bytes > r.maxBytes && r.n > 0 {
		r.evict()
	}
}

func (r *ring) evict() {
	e := &r.buf[r.head]
	r.bytes -= e.size
	*e = ringEntry{}
	r.head = (r.head + 1) % len(r.buf)
	r.n--
}

func (r *ring) at(i int) *ringEntry { return &r.buf[(r.head+i)%len(r.buf)] }

// has reports whether the event with seq is still in the ring. Ring seqs increase but are not
// contiguous: the events dropped at Publish (a hub_overflow gap) have seqs no ring entry carries.
func (r *ring) has(seq int64) bool {
	if r.n == 0 || seq < r.at(0).seq || seq > r.at(r.n-1).seq {
		return false
	}
	for i := 0; i < r.n; i++ {
		if s := r.at(i).seq; s >= seq {
			return s == seq
		}
	}
	return false
}

// after returns the entries with a seq above since, oldest first.
func (r *ring) after(since int64) []ringEntry {
	var out []ringEntry
	for i := 0; i < r.n; i++ {
		if e := r.at(i); e.seq > since {
			out = append(out, *e)
		}
	}
	return out
}

func (r *ring) oldestTS(def time.Time) time.Time {
	if r.n == 0 {
		return def
	}
	return r.at(0).ts
}

func formatTS(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
