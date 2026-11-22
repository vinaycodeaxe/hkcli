"""Minimal MQTT 3.1.1 client over WebSocket, matching what Hike Web uses.

Hike's relay (wss://relay-ws.hikeapp.com/mqtt) is MQTT 3.1.1 with one extension:
the PUBACK for a client publish may carry a payload after the packet id
(2-byte length + bytes). Hike Web's bundled mqtt-packet parser is patched to read
it, and Hike uses it to return the server message id ("seq:timestamp").
Stock MQTT libraries reject such a PUBACK, so this module implements the small
subset of MQTT the web client needs: CONNECT, SUBSCRIBE, PUBLISH (QoS 0/1),
PUBACK, PINGREQ and DISCONNECT.
"""

from __future__ import annotations

import queue
import struct
import threading
import time
from dataclasses import dataclass

import websocket

CONNECT, CONNACK, PUBLISH, PUBACK, PUBREC, PUBREL, PUBCOMP = 1, 2, 3, 4, 5, 6, 7
SUBSCRIBE, SUBACK, UNSUBSCRIBE, UNSUBACK, PINGREQ, PINGRESP, DISCONNECT = 8, 9, 10, 11, 12, 13, 14

CONNACK_REASONS = {
    1: "unacceptable protocol version",
    2: "client identifier rejected",
    3: "server unavailable",
    4: "bad user name or password",
    5: "not authorized",
}


class MQTTError(Exception):
    pass


class MQTTAuthError(MQTTError):
    def __init__(self, rc: int):
        super().__init__(f"MQTT login refused: {CONNACK_REASONS.get(rc, rc)}")
        self.rc = rc


class MQTTTimeout(MQTTError):
    pass


class MQTTDisconnected(MQTTError):
    pass


# ---- codec ----------------------------------------------------------------


def encode_remaining_length(n: int) -> bytes:
    if n < 0 or n > 268_435_455:
        raise ValueError("MQTT remaining length out of range")
    out = bytearray()
    while True:
        byte, n = n % 128, n // 128
        out.append(byte | 0x80 if n else byte)
        if not n:
            return bytes(out)


def _str(value: str) -> bytes:
    data = value.encode("utf-8")
    if len(data) > 0xFFFF:
        raise ValueError("string too long for MQTT")
    return struct.pack("!H", len(data)) + data


def _packet(ptype: int, flags: int, body: bytes) -> bytes:
    return bytes([(ptype << 4) | flags]) + encode_remaining_length(len(body)) + body


def encode_connect(client_id: str, username: str | None, password: str | None, keepalive: int = 25, clean: bool = True) -> bytes:
    flags = 0x02 if clean else 0x00
    payload = _str(client_id)
    if username is not None:
        flags |= 0x80
        payload += _str(username)
        if password is not None:
            flags |= 0x40
            payload += _str(password)
    variable = _str("MQTT") + bytes([4, flags]) + struct.pack("!H", keepalive)
    return _packet(CONNECT, 0, variable + payload)


def encode_publish(topic: str, payload: bytes, qos: int = 0, packet_id: int | None = None, dup: bool = False, retain: bool = False) -> bytes:
    flags = (0x08 if dup else 0) | (qos << 1) | (0x01 if retain else 0)
    body = _str(topic)
    if qos:
        body += struct.pack("!H", packet_id)
    return _packet(PUBLISH, flags, body + payload)


def encode_puback(packet_id: int, payload: bytes | None = None) -> bytes:
    body = struct.pack("!H", packet_id)
    if payload is not None:  # Hike extension, only sent by brokers
        body += struct.pack("!H", len(payload)) + payload
    return _packet(PUBACK, 0, body)


def encode_pubrec(packet_id: int) -> bytes:
    return _packet(PUBREC, 0, struct.pack("!H", packet_id))


def encode_pubrel(packet_id: int) -> bytes:
    return _packet(PUBREL, 0x02, struct.pack("!H", packet_id))


def encode_pubcomp(packet_id: int) -> bytes:
    return _packet(PUBCOMP, 0, struct.pack("!H", packet_id))


def encode_subscribe(packet_id: int, topic: str, qos: int = 1) -> bytes:
    return _packet(SUBSCRIBE, 0x02, struct.pack("!H", packet_id) + _str(topic) + bytes([qos]))


def encode_pingreq() -> bytes:
    return _packet(PINGREQ, 0, b"")


def encode_disconnect() -> bytes:
    return _packet(DISCONNECT, 0, b"")


def encode_connack(rc: int, session_present: bool = False) -> bytes:
    return _packet(CONNACK, 0, bytes([1 if session_present else 0, rc]))


def encode_suback(packet_id: int, codes: list[int]) -> bytes:
    return _packet(SUBACK, 0, struct.pack("!H", packet_id) + bytes(codes))


def encode_pingresp() -> bytes:
    return _packet(PINGRESP, 0, b"")


@dataclass
class Packet:
    type: int
    flags: int
    body: bytes


class PacketReader:
    """Reassembles MQTT packets from a byte stream; frames may split or batch packets."""

    def __init__(self) -> None:
        self._buf = bytearray()

    def feed(self, data: bytes) -> list[Packet]:
        self._buf += data
        out: list[Packet] = []
        while len(self._buf) >= 2:
            value, multiplier, pos = 0, 1, 1
            while True:
                if pos >= len(self._buf):
                    return out
                byte = self._buf[pos]
                value += (byte & 0x7F) * multiplier
                pos += 1
                if not byte & 0x80:
                    break
                multiplier *= 128
                if multiplier > 128**3:
                    raise MQTTError("malformed MQTT remaining length")
            end = pos + value
            if len(self._buf) < end:
                break
            first = self._buf[0]
            out.append(Packet(first >> 4, first & 0x0F, bytes(self._buf[pos:end])))
            del self._buf[:end]
        return out


def parse_connack(body: bytes) -> tuple[bool, int]:
    return bool(body[0] & 0x01), body[1]


def parse_publish(flags: int, body: bytes) -> tuple[str, bytes, int, int | None]:
    qos = (flags >> 1) & 0x03
    (topic_len,) = struct.unpack_from("!H", body, 0)
    topic = body[2 : 2 + topic_len].decode("utf-8")
    pos = 2 + topic_len
    packet_id = None
    if qos:
        (packet_id,) = struct.unpack_from("!H", body, pos)
        pos += 2
    return topic, body[pos:], qos, packet_id


def parse_puback(body: bytes) -> tuple[int, bytes | None]:
    (packet_id,) = struct.unpack_from("!H", body, 0)
    payload = None
    if len(body) >= 4:
        (size,) = struct.unpack_from("!H", body, 2)
        if size > 0:
            payload = body[4 : 4 + size]
    return packet_id, payload


def parse_suback(body: bytes) -> tuple[int, list[int]]:
    (packet_id,) = struct.unpack_from("!H", body, 0)
    return packet_id, list(body[2:])


def parse_packet_id(body: bytes) -> int:
    return struct.unpack_from("!H", body, 0)[0]


# ---- client ---------------------------------------------------------------


@dataclass
class Message:
    topic: str
    payload: bytes
    qos: int


class _Waiter:
    __slots__ = ("event", "result", "error")

    def __init__(self) -> None:
        self.event = threading.Event()
        self.result = None
        self.error: str | None = None


class MQTTClient:
    """Thread-based client. Incoming publishes land in `incoming`; None there means the link dropped."""

    POLL_SECONDS = 2.0

    def __init__(
        self,
        url: str,
        *,
        client_id: str,
        username: str | None,
        password: str | None,
        keepalive: int = 25,
        origin: str | None = None,
        headers: list[str] | None = None,
        connect_timeout: float = 15.0,
    ):
        self.url = url
        self.client_id = client_id
        self.username = username
        self.password = password
        self.keepalive = keepalive
        self.origin = origin
        self.headers = headers or []
        self.connect_timeout = connect_timeout
        self.incoming: "queue.Queue[Message | None]" = queue.Queue()
        self.disconnect_reason: str | None = None
        self._ws = None
        self._reader = PacketReader()
        self._early: list[Packet] = []
        self._send_lock = threading.Lock()
        self._state_lock = threading.Lock()
        self._pending: dict[int, _Waiter] = {}
        self._next_id = 0
        self._closing = False
        self._closed = threading.Event()
        self._thread: threading.Thread | None = None
        self._last_send = 0.0
        self._ping_sent_at: float | None = None

    # -- lifecycle --

    def _open_websocket(self):
        options = dict(timeout=self.connect_timeout, origin=self.origin, header=self.headers, enable_multithread=True)
        try:
            return websocket.create_connection(self.url, subprotocols=["mqtt"], **options)
        except websocket.WebSocketException as exc:
            # Browsers accept a handshake that omits the subprotocol; websocket-client does not.
            if "subprotocol" not in str(exc).lower():
                raise
            return websocket.create_connection(self.url, **options)

    def connect(self) -> None:
        try:
            self._ws = self._open_websocket()
        except Exception as exc:
            raise MQTTError(f"cannot open MQTT websocket {self.url}: {exc}") from exc
        try:
            self._raw_send(encode_connect(self.client_id, self.username, self.password, self.keepalive))
            deadline = time.monotonic() + self.connect_timeout
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise MQTTTimeout("timed out waiting for the broker to accept the login")
                self._ws.settimeout(remaining)
                try:
                    data = self._recv_frame()
                except websocket.WebSocketTimeoutException as exc:
                    raise MQTTTimeout("timed out waiting for the broker to accept the login") from exc
                if data is None:
                    raise MQTTError(f"broker closed the connection during login ({self.disconnect_reason})")
                packets = self._reader.feed(data)
                for index, pkt in enumerate(packets):
                    if pkt.type != CONNACK:
                        continue
                    _, rc = parse_connack(pkt.body)
                    if rc in (4, 5):
                        raise MQTTAuthError(rc)
                    if rc != 0:
                        raise MQTTError(f"MQTT login refused: {CONNACK_REASONS.get(rc, rc)}")
                    self._early = packets[index + 1 :]
                    self._ws.settimeout(self.POLL_SECONDS)
                    self._thread = threading.Thread(target=self._run, name="hikecli-mqtt", daemon=True)
                    self._thread.start()
                    return
        except MQTTError:
            self._shutdown_socket()
            raise
        except Exception as exc:
            self._shutdown_socket()
            raise MQTTError(f"MQTT login failed: {exc}") from exc
        except BaseException:
            self._shutdown_socket()
            raise

    def disconnect(self) -> None:
        if self._ws is None:
            return
        self._closing = True
        if not self._closed.is_set():
            try:
                self._raw_send(encode_disconnect())
            except MQTTError:
                pass
        self._shutdown_socket()
        if self._thread and self._thread is not threading.current_thread():
            self._thread.join(timeout=5)
        self._finish(self.disconnect_reason or "client disconnected")

    @property
    def connected(self) -> bool:
        return self._ws is not None and not self._closed.is_set()

    def __enter__(self) -> "MQTTClient":
        self.connect()
        return self

    def __exit__(self, *exc) -> None:
        self.disconnect()

    # -- operations --

    def subscribe(self, topic: str, qos: int = 1, timeout: float = 10.0) -> int:
        packet_id, waiter = self._register()
        self._raw_send(encode_subscribe(packet_id, topic, qos))
        codes = self._wait(packet_id, waiter, timeout, "SUBACK")
        if not codes or codes[0] == 0x80:
            raise MQTTError(f"broker rejected the subscription to {topic}")
        return codes[0]

    def publish(self, topic: str, payload: bytes | str, qos: int = 0, timeout: float = 10.0) -> bytes | None:
        """Publish; for QoS 1 wait for PUBACK and return its Hike payload (or None)."""
        data = payload.encode("utf-8") if isinstance(payload, str) else payload
        if qos == 0:
            self._raw_send(encode_publish(topic, data, 0))
            return None
        if qos != 1:
            raise ValueError("only QoS 0 and 1 are supported")
        packet_id, waiter = self._register()
        self._raw_send(encode_publish(topic, data, 1, packet_id))
        return self._wait(packet_id, waiter, timeout, "PUBACK")

    # -- internals --

    def _recv_frame(self) -> bytes | None:
        """Next data frame, or None when the peer closed the websocket."""
        while True:
            opcode, data = self._ws.recv_data(control_frame=True)
            if opcode in (websocket.ABNF.OPCODE_BINARY, websocket.ABNF.OPCODE_TEXT):
                return data if isinstance(data, bytes) else bytes(data)
            if opcode == websocket.ABNF.OPCODE_CLOSE:
                code = struct.unpack("!H", data[:2])[0] if data and len(data) >= 2 else None
                self.disconnect_reason = f"broker closed the websocket (code {code})"
                return None

    def _run(self) -> None:
        reason = None
        try:
            for pkt in self._early:
                self._dispatch(pkt)
            while not self._closing:
                try:
                    data = self._recv_frame()
                except (websocket.WebSocketTimeoutException, TimeoutError):
                    self._keepalive()
                    continue
                if data is None:
                    reason = self.disconnect_reason
                    break
                for pkt in self._reader.feed(data):
                    self._dispatch(pkt)
                self._keepalive()
        except Exception as exc:  # any socket failure means the link is gone
            if not self._closing:
                reason = f"connection lost: {exc}"
        finally:
            self._finish(reason)

    def _keepalive(self) -> None:
        now = time.monotonic()
        if self._ping_sent_at is not None:
            if now - self._ping_sent_at > max(self.keepalive, 10):
                raise MQTTDisconnected("broker stopped answering keepalive pings")
            return
        if self.keepalive and now - self._last_send >= self.keepalive * 0.75:
            self._ping_sent_at = now
            self._raw_send(encode_pingreq())

    def _dispatch(self, pkt: Packet) -> None:
        if pkt.type == PUBLISH:
            topic, payload, qos, packet_id = parse_publish(pkt.flags, pkt.body)
            self.incoming.put(Message(topic, payload, qos))  # queue before acking so a failed ack loses nothing
            if qos == 1:
                self._raw_send(encode_puback(packet_id))
            elif qos == 2:
                self._raw_send(encode_pubrec(packet_id))
        elif pkt.type == PUBACK:
            packet_id, payload = parse_puback(pkt.body)
            self._resolve(packet_id, payload)
        elif pkt.type == SUBACK:
            packet_id, codes = parse_suback(pkt.body)
            self._resolve(packet_id, codes)
        elif pkt.type == PUBREL:
            self._raw_send(encode_pubcomp(parse_packet_id(pkt.body)))
        elif pkt.type == PINGRESP:
            self._ping_sent_at = None

    def _register(self) -> tuple[int, _Waiter]:
        with self._state_lock:
            if self._closed.is_set():
                raise MQTTDisconnected(self.disconnect_reason or "not connected")
            for _ in range(65535):
                self._next_id = self._next_id % 65535 + 1
                if self._next_id not in self._pending:
                    break
            waiter = _Waiter()
            self._pending[self._next_id] = waiter
            return self._next_id, waiter

    def _resolve(self, packet_id: int, result) -> None:
        with self._state_lock:
            waiter = self._pending.pop(packet_id, None)
        if waiter:
            waiter.result = result
            waiter.event.set()

    def _wait(self, packet_id: int, waiter: _Waiter, timeout: float, what: str):
        if not waiter.event.wait(timeout):
            with self._state_lock:
                self._pending.pop(packet_id, None)
            raise MQTTTimeout(f"timed out waiting for {what} from the broker")
        if waiter.error:
            raise MQTTDisconnected(waiter.error)
        return waiter.result

    def _raw_send(self, data: bytes) -> None:
        ws = self._ws
        if ws is None or self._closed.is_set():
            raise MQTTDisconnected(self.disconnect_reason or "not connected")
        try:
            with self._send_lock:
                ws.send(data, opcode=websocket.ABNF.OPCODE_BINARY)
                self._last_send = time.monotonic()
        except MQTTError:
            raise
        except Exception as exc:
            self._shutdown_socket()  # a partial write corrupts the stream; let the reader wind down
            raise MQTTDisconnected(f"send failed: {exc}") from exc

    def _shutdown_socket(self) -> None:
        ws = self._ws
        if ws is not None:
            try:
                ws.shutdown()
            except Exception:
                pass

    def _finish(self, reason: str | None) -> None:
        with self._state_lock:
            if self._closed.is_set():
                return
            self._closed.set()
            self.disconnect_reason = reason or self.disconnect_reason or "disconnected"
            pending, self._pending = self._pending, {}
        for waiter in pending.values():
            waiter.error = self.disconnect_reason
            waiter.event.set()
        self._shutdown_socket()
        self.incoming.put(None)
