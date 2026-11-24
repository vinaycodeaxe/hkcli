package mqtt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Message is an incoming PUBLISH.
type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
}

// Client is an MQTT session over WebSocket: set the exported fields, then
// Connect. Incoming publishes are queued for Next.
type Client struct {
	URL       string
	ClientID  string
	Username  string
	Password  string
	Keepalive time.Duration
	Origin    string
	UserAgent string

	ws       *websocket.Conn
	reader   PacketReader
	early    []Packet
	sendSlot chan struct{} // held while a frame is written: one write at a time
	lastSend atomic.Int64  // when the last write finished, in Unix nanoseconds

	mu        sync.Mutex
	pending   map[uint16]*waiter
	nextID    uint16
	closing   bool
	closed    bool
	reason    string
	pingSent  time.Time
	inbox     []*Message
	notify    chan struct{}
	readerEnd chan struct{}
	stopPing  chan struct{}
}

type waiter struct {
	done   chan struct{}
	result any
	err    string
}

const connectTimeout = 15 * time.Second

// errConnectionLost is how a dropped TCP connection is reported in status lines.
var errConnectionLost = errors.New("Connection to remote host was lost.")

// Connect opens the WebSocket, logs in and starts reading. A refused login
// returns an Error of KindAuth.
func (c *Client) Connect(ctx context.Context) error {
	ws, err := c.dial(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newError(KindError, "cannot open MQTT websocket %s: %v", c.URL, err)
	}
	c.ws = ws
	c.sendSlot = make(chan struct{}, 1)
	c.pending = map[uint16]*waiter{}
	c.notify = make(chan struct{}, 1)
	c.readerEnd = make(chan struct{})
	c.stopPing = make(chan struct{})
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	err = c.login()
	stop()
	if err != nil {
		ws.Close()
		c.ws = nil
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	go c.run()
	go c.keepalive()
	return nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	header := http.Header{}
	header.Set("Origin", c.Origin)
	header.Set("User-Agent", c.UserAgent)
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: connectTimeout,
		NetDialContext:   (&net.Dialer{Timeout: connectTimeout}).DialContext,
		Subprotocols:     []string{"mqtt"},
	}
	ws, resp, err := dialer.DialContext(ctx, c.URL, header)
	if errors.Is(err, websocket.ErrBadHandshake) {
		// Some servers refuse a subprotocol they do not know; browsers then retry without one.
		dialer.Subprotocols = nil
		ws, resp, err = dialer.DialContext(ctx, c.URL, header)
	}
	if err != nil && resp != nil {
		err = fmt.Errorf("%w (HTTP %d)", err, resp.StatusCode)
	}
	return ws, err
}

func (c *Client) login() error {
	if err := c.rawSend(EncodeConnect(c.ClientID, c.Username, c.Password, uint16(c.Keepalive/time.Second))); err != nil {
		return err
	}
	_ = c.ws.SetReadDeadline(time.Now().Add(connectTimeout))
	for {
		data, err := c.readFrame()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return newError(KindTimeout, "timed out waiting for the broker to accept the login")
			}
			return newError(KindError, "MQTT login failed: %v", err)
		}
		if data == nil {
			return newError(KindError, "broker closed the connection during login (%s)", c.reason)
		}
		packets, err := c.reader.Feed(data)
		if err != nil {
			return newError(KindError, "MQTT login failed: %v", err)
		}
		for i, p := range packets {
			if p.Type != CONNACK {
				continue
			}
			if len(p.Body) < 2 {
				return newError(KindError, "MQTT login failed: short CONNACK")
			}
			switch rc := p.Body[1]; rc {
			case 0:
				c.early = packets[i+1:]
				_ = c.ws.SetReadDeadline(time.Time{})
				return nil
			case 4, 5:
				return newError(KindAuth, "MQTT login refused: %s", connackReason(rc))
			default:
				return newError(KindError, "MQTT login refused: %s", connackReason(rc))
			}
		}
	}
}

// readFrame returns the next data frame, or nil when the peer closed the WebSocket.
func (c *Client) readFrame() ([]byte, error) {
	_, data, err := c.ws.ReadMessage()
	if err == nil {
		return data, nil
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		if ce.Code == websocket.CloseAbnormalClosure {
			return nil, errConnectionLost
		}
		code := fmt.Sprint(ce.Code)
		if ce.Code == websocket.CloseNoStatusReceived {
			code = "None"
		}
		c.mu.Lock()
		c.reason = "broker closed the websocket (code " + code + ")"
		c.mu.Unlock()
		return nil, nil
	}
	return nil, err
}

func (c *Client) run() {
	defer close(c.readerEnd)
	reason, err := "", c.dispatchAll(c.early)
	for err == nil {
		var data []byte
		data, err = c.readFrame()
		if err != nil {
			break
		}
		if data == nil {
			c.mu.Lock()
			reason = c.reason
			c.mu.Unlock()
			break
		}
		var packets []Packet
		if packets, err = c.reader.Feed(data); err == nil {
			err = c.dispatchAll(packets)
		}
	}
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if err != nil && !closing {
		reason = "connection lost: " + err.Error()
	}
	c.finish(reason)
}

// keepalive sends PINGREQ when 75% of the keepalive passed since the last send,
// and drops the link if the broker stops answering.
func (c *Client) keepalive() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopPing:
			return
		case <-ticker.C:
		}
		now := time.Now()
		c.mu.Lock()
		pingSent := c.pingSent
		c.mu.Unlock()
		if !pingSent.IsZero() {
			if now.Sub(pingSent) > max(c.Keepalive, 10*time.Second) {
				c.finish("connection lost: broker stopped answering keepalive pings")
				return
			}
			continue
		}
		idle := now.Sub(time.Unix(0, c.lastSend.Load()))
		if c.Keepalive > 0 && idle >= c.Keepalive*3/4 {
			c.mu.Lock()
			c.pingSent = now
			c.mu.Unlock()
			if err := c.rawSend(packet(PINGREQ, 0, nil)); err != nil {
				c.finish("connection lost: " + err.Error())
				return
			}
		}
	}
}

func (c *Client) dispatchAll(packets []Packet) error {
	for _, p := range packets {
		if err := c.dispatch(p); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) dispatch(p Packet) error {
	switch p.Type {
	case PUBLISH:
		topic, payload, qos, id, err := parsePublish(p.Flags, p.Body)
		if err != nil {
			return err
		}
		c.enqueue(&Message{Topic: topic, Payload: payload, QoS: qos}) // queue before acking so a failed ack loses nothing
		switch qos {
		case 1:
			return c.rawSend(encodePacketID(PUBACK, 0, id))
		case 2:
			return c.rawSend(encodePacketID(PUBREC, 0, id))
		}
	case PUBACK:
		id, payload, err := ParsePuback(p.Body)
		if err != nil {
			return err
		}
		c.resolve(id, payload)
	case SUBACK:
		id, err := packetID(p.Body)
		if err != nil {
			return err
		}
		c.resolve(id, p.Body[2:])
	case PUBREL:
		id, err := packetID(p.Body)
		if err != nil {
			return err
		}
		return c.rawSend(encodePacketID(PUBCOMP, 0, id))
	case PINGRESP:
		c.mu.Lock()
		c.pingSent = time.Time{}
		c.mu.Unlock()
	}
	return nil
}

func (c *Client) enqueue(m *Message) {
	c.mu.Lock()
	c.inbox = append(c.inbox, m)
	c.mu.Unlock()
	c.wake()
}

func (c *Client) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// Next returns the next incoming message, waiting up to timeout. It returns
// (nil, nil) on timeout and a KindDisconnected error once the link is gone and
// every queued message was read.
func (c *Client) Next(ctx context.Context, timeout time.Duration) (*Message, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if len(c.inbox) > 0 {
			m := c.inbox[0]
			c.inbox = c.inbox[1:]
			c.mu.Unlock()
			return m, nil
		}
		closed, reason := c.closed, c.reason
		c.mu.Unlock()
		if closed {
			return nil, newError(KindDisconnected, "%s", reason)
		}
		select {
		case <-c.notify:
		case <-timer.C:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *Client) register() (uint16, *waiter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, newError(KindDisconnected, "%s", orDefault(c.reason, "not connected"))
	}
	for range 65535 {
		c.nextID = c.nextID%65535 + 1
		if _, busy := c.pending[c.nextID]; !busy {
			break
		}
	}
	w := &waiter{done: make(chan struct{})}
	c.pending[c.nextID] = w
	return c.nextID, w, nil
}

func (c *Client) resolve(id uint16, result any) {
	c.mu.Lock()
	w := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if w != nil {
		w.result = result
		close(w.done)
	}
}

func (c *Client) wait(ctx context.Context, id uint16, w *waiter, timeout time.Duration, what string) (any, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.done:
		if w.err != "" {
			return nil, newError(KindDisconnected, "%s", w.err)
		}
		return w.result, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, newError(KindTimeout, "timed out waiting for %s from the broker", what)
}

// Subscribe subscribes to topic and returns the granted QoS.
func (c *Client) Subscribe(ctx context.Context, topic string, qos byte) (byte, error) {
	id, w, err := c.register()
	if err != nil {
		return 0, err
	}
	if err := c.send(ctx, encodeSubscribe(id, topic, qos)); err != nil {
		c.forget(id)
		return 0, err
	}
	result, err := c.wait(ctx, id, w, 10*time.Second, "SUBACK")
	if err != nil {
		return 0, err
	}
	codes, _ := result.([]byte)
	if len(codes) == 0 || codes[0] == 0x80 {
		return 0, newError(KindError, "broker rejected the subscription to %s", topic)
	}
	return codes[0], nil
}

// Publish sends payload to topic. For QoS 1 it waits up to timeout for the
// PUBACK and returns Hike's payload from it (nil when there is none). When ctx
// ends while another write still holds the connection, it gives up before
// writing anything.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, qos byte, timeout time.Duration) ([]byte, error) {
	if qos == 0 {
		return nil, c.send(ctx, encodePublish(topic, payload, 0, 0))
	}
	id, w, err := c.register()
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, encodePublish(topic, payload, 1, id)); err != nil {
		c.forget(id)
		return nil, err
	}
	result, err := c.wait(ctx, id, w, timeout, "PUBACK")
	ack, _ := result.([]byte)
	return ack, err
}

func (c *Client) forget(id uint16) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// rawSend writes a frame the client sends on its own behalf.
func (c *Client) rawSend(data []byte) error { return c.send(context.Background(), data) }

// send writes one frame. It waits for the connection's write slot only while ctx lasts, so a
// queued frame is never written after its caller gave up. A write that started is not cut short
// by ctx: a partial frame would break the connection for everyone using it.
func (c *Client) send(ctx context.Context, data []byte) error {
	c.mu.Lock()
	closed, reason := c.closed, c.reason
	c.mu.Unlock()
	if c.ws == nil || closed {
		return newError(KindDisconnected, "%s", orDefault(reason, "not connected"))
	}
	if err := c.acquireSendSlot(ctx); err != nil {
		return err
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	err := c.ws.WriteMessage(websocket.BinaryMessage, data)
	if err == nil {
		c.lastSend.Store(time.Now().UnixNano())
	}
	<-c.sendSlot
	if err != nil {
		c.ws.Close() // a partial write corrupts the stream; let the reader wind down
		return newError(KindDisconnected, "send failed: %v", err)
	}
	return nil
}

// acquireSendSlot waits for the write slot until ctx ends; an ended ctx never gets it.
func (c *Client) acquireSendSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.sendSlot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-c.sendSlot
		return err
	}
	return nil
}

// Connected reports whether the link is still up.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws != nil && !c.closed
}

// Disconnect sends DISCONNECT and closes the link.
func (c *Client) Disconnect() {
	if c.ws == nil {
		return
	}
	c.mu.Lock()
	c.closing = true
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		_ = c.rawSend(packet(DISCONNECT, 0, nil))
	}
	c.ws.Close()
	select {
	case <-c.readerEnd:
	case <-time.After(5 * time.Second):
	}
	c.mu.Lock()
	reason := orDefault(c.reason, "client disconnected")
	c.mu.Unlock()
	c.finish(reason)
}

// Close drops the link at once, without the DISCONNECT that Disconnect writes first (that write
// can wait behind a stuck one for its whole deadline). Calls still waiting on the broker fail. It
// is safe to call more than once, also while Disconnect runs.
func (c *Client) Close() {
	if c.ws == nil {
		return
	}
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	c.ws.Close()
	select {
	case <-c.readerEnd:
	case <-time.After(5 * time.Second):
	}
	c.mu.Lock()
	reason := orDefault(c.reason, "client closed")
	c.mu.Unlock()
	c.finish(reason)
}

func (c *Client) finish(reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.reason = orDefault(orDefault(reason, c.reason), "disconnected")
	pending := c.pending
	c.pending = map[uint16]*waiter{}
	c.mu.Unlock()
	for _, w := range pending {
		w.err = c.reason
		close(w.done)
	}
	close(c.stopPing)
	c.ws.Close()
	c.wake()
}

func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
