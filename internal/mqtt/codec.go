// Package mqtt is a minimal MQTT 3.1.1 client over WebSocket, matching what
// Hike Web uses.
//
// Hike's relay (wss://relay-ws.hikeapp.com/mqtt) is MQTT 3.1.1 with one
// extension: the PUBACK for a client publish may carry a payload after the
// packet id (2-byte length + bytes) holding the server message id
// ("seq:timestamp"). Stock MQTT libraries reject such a PUBACK, so this package
// implements the small subset the web client needs: CONNECT, SUBSCRIBE,
// PUBLISH (QoS 0/1), PUBACK, PINGREQ and DISCONNECT.
package mqtt

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Packet types.
const (
	CONNECT    = 1
	CONNACK    = 2
	PUBLISH    = 3
	PUBACK     = 4
	PUBREC     = 5
	PUBREL     = 6
	PUBCOMP    = 7
	SUBSCRIBE  = 8
	SUBACK     = 9
	PINGREQ    = 12
	PINGRESP   = 13
	DISCONNECT = 14
)

var connackReasons = map[byte]string{
	1: "unacceptable protocol version",
	2: "client identifier rejected",
	3: "server unavailable",
	4: "bad user name or password",
	5: "not authorized",
}

func connackReason(rc byte) string {
	if r, ok := connackReasons[rc]; ok {
		return r
	}
	return fmt.Sprint(rc)
}

// Kind classifies relay failures.
type Kind int

const (
	KindError        Kind = iota // any other relay failure
	KindAuth                     // the broker refused the login (CONNACK 4 or 5)
	KindTimeout                  // no answer in time
	KindDisconnected             // the link is gone
)

// Error is a relay failure.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func newError(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// IsKind reports whether err is a relay error of the given kind.
func IsKind(err error, kind Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

// ---- codec -----------------------------------------------------------------

// EncodeRemainingLength encodes n as MQTT's variable-length integer.
func EncodeRemainingLength(n int) ([]byte, error) {
	if n < 0 || n > 268_435_455 {
		return nil, errors.New("MQTT remaining length out of range")
	}
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out, nil
		}
	}
}

func appendString(dst []byte, s string) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, s...)
}

func packet(ptype, flags byte, body []byte) []byte {
	length, err := EncodeRemainingLength(len(body))
	if err != nil {
		panic(err) // callers never build packets near 256 MB
	}
	return append(append([]byte{ptype<<4 | flags}, length...), body...)
}

// EncodeConnect builds a clean-session CONNECT with user name and password.
func EncodeConnect(clientID, username, password string, keepalive uint16) []byte {
	variable := appendString(nil, "MQTT")
	variable = append(variable, 4, 0x02|0x80|0x40)
	variable = binary.BigEndian.AppendUint16(variable, keepalive)
	payload := appendString(nil, clientID)
	payload = appendString(payload, username)
	payload = appendString(payload, password)
	return packet(CONNECT, 0, append(variable, payload...))
}

func encodePublish(topic string, payload []byte, qos byte, packetID uint16) []byte {
	body := appendString(nil, topic)
	if qos > 0 {
		body = binary.BigEndian.AppendUint16(body, packetID)
	}
	return packet(PUBLISH, qos<<1, append(body, payload...))
}

func encodePacketID(ptype, flags byte, packetID uint16) []byte {
	return packet(ptype, flags, binary.BigEndian.AppendUint16(nil, packetID))
}

func encodeSubscribe(packetID uint16, topic string, qos byte) []byte {
	body := binary.BigEndian.AppendUint16(nil, packetID)
	body = appendString(body, topic)
	return packet(SUBSCRIBE, 0x02, append(body, qos))
}

// Packet is one decoded MQTT packet.
type Packet struct {
	Type  byte
	Flags byte
	Body  []byte
}

// PacketReader reassembles MQTT packets from a byte stream; WebSocket frames
// may split or batch packets.
type PacketReader struct {
	buf []byte
}

// Feed adds data and returns every packet completed so far.
func (r *PacketReader) Feed(data []byte) ([]Packet, error) {
	r.buf = append(r.buf, data...)
	var out []Packet
	for len(r.buf) >= 2 {
		value, multiplier, pos := 0, 1, 1
		for {
			if pos >= len(r.buf) {
				return out, nil
			}
			b := r.buf[pos]
			value += int(b&0x7f) * multiplier
			pos++
			if b&0x80 == 0 {
				break
			}
			multiplier *= 128
			if multiplier > 128*128*128 {
				return out, newError(KindError, "malformed MQTT remaining length")
			}
		}
		end := pos + value
		if len(r.buf) < end {
			break
		}
		body := append([]byte(nil), r.buf[pos:end]...)
		out = append(out, Packet{Type: r.buf[0] >> 4, Flags: r.buf[0] & 0x0f, Body: body})
		r.buf = r.buf[end:]
	}
	return out, nil
}

var errShortPacket = errors.New("MQTT packet is too short")

func packetID(body []byte) (uint16, error) {
	if len(body) < 2 {
		return 0, errShortPacket
	}
	return binary.BigEndian.Uint16(body), nil
}

// ParsePuback returns the packet id and Hike's optional payload (nil if none).
func ParsePuback(body []byte) (uint16, []byte, error) {
	id, err := packetID(body)
	if err != nil {
		return 0, nil, err
	}
	var payload []byte
	if len(body) >= 4 {
		if size := int(binary.BigEndian.Uint16(body[2:])); size > 0 {
			payload = body[4:min(len(body), 4+size)]
		}
	}
	return id, payload, nil
}

func parsePublish(flags byte, body []byte) (topic string, payload []byte, qos byte, id uint16, err error) {
	qos = (flags >> 1) & 0x03
	n, err := packetID(body)
	if err != nil || len(body) < 2+int(n) {
		return "", nil, 0, 0, errShortPacket
	}
	topic, pos := string(body[2:2+int(n)]), 2+int(n)
	if qos > 0 {
		if id, err = packetID(body[pos:]); err != nil {
			return "", nil, 0, 0, err
		}
		pos += 2
	}
	return topic, body[pos:], qos, id, nil
}
