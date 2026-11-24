"""In-process fake of Hike's REST API, QR-login socket and MQTT relay, for end-to-end tests.

It follows the wire formats Hike Web uses (see README "How it works"), and parses
MQTT independently of hkcli's encoder so codec bugs are not hidden.
"""

from __future__ import annotations

import json
import math
import struct
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from websockets.sync.server import serve

USER, PEER, PEER2, NEW_PEER, GROUP = "1001", "2002", "2003", "2009", "g3003"
T0 = 1_760_000_000_000


def _pkt(sender, to, text, seq, ts, *, st="text", d=None, conv=None, **extra):
    data = {"st": st, "ts": ts, "m": text, "p": text}
    data.update(d or {})
    packet = {"t": "m", "st": st, "f": sender, "to": to, "c": conv or (to if to != USER else sender), "i": str(uuid.uuid4()), "s": f"{seq}:{ts}", "ts": ts, "d": data}
    packet.update(extra)
    return packet


def _item(packet, state="DELIVERED", **meta):
    return {"messageJson": json.dumps(packet), "state": state, **meta}


class FakeHike:
    def __init__(self):
        self.lock = threading.RLock()
        self.access_token, self.refresh_token, self.generation = "access-1", "refresh-1", 1
        self.access_valid = True
        self.refresh_calls = 0
        self.http_log: list[dict] = []
        self.published: list[tuple[str, dict]] = []
        self.mqtt_connects: list[dict] = []
        self.sessions: list[dict] = []
        self.open_relays = 0  # accepted relay connections that are still open
        self.peak_open_relays = 0
        self.puback_delay = 0.0  # seconds Hike takes to acknowledge a chat message
        self.fail_conversations = 0  # how many chat list requests fail (a failed catch-up)
        self.qr_device_ids: list[str] = []
        self.otp_requests: list[dict] = []
        self.logout_requests: list[dict] = []
        self.uploads: dict[str, dict] = {}
        self.files = {"file-1": bytes(range(48, 48 + 30))}
        self.seq = 900
        self._pid = 0
        self._build_data()

    # ---- data ---------------------------------------------------------------

    def _build_data(self):
        older = [_pkt(PEER, USER, f"old message {n}", n, T0 - (20 - n) * 60_000) for n in range(1, 13)]
        recent = [
            _item(_pkt(PEER, USER, "hello from asha", 20, T0 + 1_000)),
            _item(_pkt(USER, PEER, "hi asha, lunch tomorrow?", 21, T0 + 2_000), state="READ"),
            _item(_pkt(PEER, USER, "menu photo", 22, T0 + 3_000, st="img", d={"fid": "file-1", "s": 30, "w": 10, "h": 10, "mime": "image/jpeg"})),
            _item(_pkt(PEER, USER, "see you at noon", 23, T0 + 4_000), reactionMap={USER: "👍"}),
            _item(_pkt(PEER, USER, "secret plan", 24, T0 + 5_000), deletedForAll=True),
        ]
        self.history = {PEER: list(reversed(recent)) + [_item(p) for p in reversed(older)]}
        group_msgs = [
            {"t": "gca", "st": "gcj", "f": USER, "to": GROUP, "c": GROUP, "s": f"30:{T0}", "ts": T0, "d": {"gid": GROUP, "nm": [{"uid": PEER2, "n": "Ravi", "r": "MEMBER"}]}},
            _pkt(PEER2, GROUP, "family dinner on sunday", 31, T0 + 6_000, conv=GROUP, pi={"n": "Ravi", "p": "+919800000003"}),
        ]
        self.history[GROUP] = [_item(p) for p in reversed(group_msgs)]
        last_peer = json.loads(self.history[PEER][0]["messageJson"])
        last_group = json.loads(self.history[GROUP][0]["messageJson"])
        self.conversations = [
            {"conversationId": USER, "conversationType": "ONE_ON_ONE", "conversationName": "Me"},
            {
                "conversationId": PEER, "conversationType": "ONE_ON_ONE", "conversationName": "Asha", "phoneNumber": "+919800000002",
                "unreadCount": 2, "undelivered": True, "lastMessage": json.dumps(last_peer), "lastMessageSentBy": PEER,
                "lastMessageTs": last_peer["ts"] + 3, "lastMessageId": last_peer["s"], "lastMessageIsDeletedForAll": True,
            },
            {
                "conversationId": GROUP, "conversationType": "GROUP", "conversationName": "Family", "unreadCount": 0,
                "lastMessage": last_group, "lastMessageSentBy": PEER2, "lastMessageTs": last_group["ts"], "lastMessageId": last_group["s"], "muted": True,
            },
        ]
        self.contacts = [{"userId": PEER, "nickName": "Asha", "phoneNumber": "+919800000002", "status": "busy", "pic": None, "isDeleted": False}]
        self.profiles = {
            PEER2: {"userId": PEER2, "name": "Ravi", "phone": "+919800000003"},
            NEW_PEER: {"userId": NEW_PEER, "name": "Neel", "phone": "+919800000009"},
        }
        self.group = {
            "id": GROUP, "name": "Family", "description": "family chat", "profileIconUrl": None, "createdBy": USER,
            "createdAt": "2025-01-01T00:00:00Z", "updatedAt": "2025-01-02T00:00:00Z", "serverId": None,
            "members": [
                {"userId": USER, "role": "OWNER", "name": "Me", "phoneNumber": "+919800000001", "addedBy": USER, "createdAt": "x"},
                {"userId": PEER2, "role": "MEMBER", "name": "Ravi", "phoneNumber": "+919800000003", "addedBy": USER, "createdAt": "x"},
            ],
        }

    # ---- lifecycle ------------------------------------------------------------

    def start(self):
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                fake._http(self, "GET")

            def do_POST(self):
                fake._http(self, "POST")

            def do_PUT(self):
                fake._http(self, "PUT")

        self.http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.qr = serve(self._qr_handler, "127.0.0.1", 0)
        self.relay = serve(self._mqtt_handler, "127.0.0.1", 0, subprotocols=["mqtt"])
        for server in (self.http, self.qr, self.relay):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        self.api_base = f"http://127.0.0.1:{self.http.server_address[1]}"
        self.session_ws = f"ws://127.0.0.1:{self.qr.socket.getsockname()[1]}"
        self.mqtt_ws = f"ws://127.0.0.1:{self.relay.socket.getsockname()[1]}/mqtt"
        return self

    def stop(self):
        self.http.shutdown()
        self.qr.shutdown()
        self.relay.shutdown()

    def env(self) -> dict:
        return {"HKCLI_API_BASE": self.api_base, "HKCLI_SESSION_WS": self.session_ws, "HKCLI_MQTT_WS": self.mqtt_ws}

    # ---- test controls ----------------------------------------------------------

    def expire_access(self):
        with self.lock:
            self.access_valid = False

    def packets(self, kind=None) -> list[dict]:
        with self.lock:
            return [p for _, p in self.published if kind is None or p.get("t") == kind]

    def wait_for(self, predicate, timeout=10.0):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if predicate():
                return True
            time.sleep(0.05)
        return False

    def kick(self) -> None:
        """Drop every relay connection, like a network blip."""
        with self.lock:
            sessions = list(self.sessions)
        for session in sessions:
            session["ws"].close()

    def reset_relay_peak(self) -> None:
        with self.lock:
            self.peak_open_relays = self.open_relays

    def push(self, packet: dict) -> None:
        """Deliver a packet to every subscribed relay session (QoS 1)."""
        body = json.dumps(packet).encode()
        with self.lock:
            targets = [s for s in self.sessions if f"u:{USER}" in s["topics"]]
        for session in targets:
            self._pid += 1
            topic = f"u:{USER}".encode()
            var = struct.pack("!H", len(topic)) + topic + struct.pack("!H", self._pid)
            try:
                session["ws"].send(self._frame(0x32, var + body))
            except Exception:
                pass  # connection is closing; the client catches up over REST after reconnecting

    # ---- HTTP -----------------------------------------------------------------------

    def _reply(self, h, status, payload=None, headers=None, raw: bytes | None = None):
        body = raw if raw is not None else (json.dumps(payload).encode() if payload is not None else b"")
        h.send_response(status)
        for key, value in (headers or {}).items():
            h.send_header(key, value)
        if raw is None:
            h.send_header("Content-Type", "application/json")
        h.send_header("Content-Length", str(len(body)))
        h.end_headers()
        h.wfile.write(body)

    def _authorized(self, h) -> bool:
        return (
            self.access_valid
            and h.headers.get("Authorization") == self.access_token
            and h.headers.get("X-User-Id") == USER
            and h.headers.get("X-App-Id") == "hike-messenger"
        )

    def _http(self, h, method):
        url = urlparse(h.path)
        path, query = url.path, parse_qs(url.query)
        length = int(h.headers.get("Content-Length") or 0)
        raw = h.rfile.read(length) if length else b""
        with self.lock:
            self.http_log.append({"method": method, "path": path, "headers": dict(h.headers), "query": query})
        body = {}
        if raw and h.headers.get("Content-Type", "").startswith("application/json"):
            body = json.loads(raw)

        if path.startswith("/blob/"):
            data = self.files[path.split("/")[-1]]
            start, end = (int(x) for x in h.headers["Range"].split("=")[1].split("-"))
            return self._reply(h, 206, raw=data[start : end + 1], headers={"Content-Range": f"bytes {start}-{end}/{len(data)}"})
        if path.startswith("/put/"):
            _, _, sid, index = path.split("/")
            with self.lock:
                self.uploads[sid]["chunks"][int(index)] = raw
            return self._reply(h, 200, raw=b"", headers={"ETag": f'"etag-{index}"'})

        if path == "/auth/v1/initiate":
            self.otp_requests.append(body)
            return self._reply(h, 200, {"success": True, "data": {"challengeId": "ch-1", "expiresInSeconds": 60}})
        if path == "/auth/v1/verify":
            if body.get("otp") != "123456" or body.get("challengeId") != "ch-1":
                return self._reply(h, 400, {"success": False, "code": "INVALID_OTP", "message": "Invalid OTP"})
            return self._reply(h, 200, {"success": True, "data": self._login_payload()})
        if path == "/auth/v1/refresh":
            with self.lock:
                self.refresh_calls += 1
                if body.get("refreshToken") != self.refresh_token:
                    return self._reply(h, 401, {"success": False, "code": "UNAUTHORIZED", "message": "Invalid refresh token"})
                self.generation += 1
                self.access_token, self.refresh_token = f"access-{self.generation}", f"refresh-{self.generation}"
                self.access_valid = True
                return self._reply(h, 200, {"success": True, "data": {"accessToken": self.access_token, "refreshToken": self.refresh_token}})

        if not self._authorized(h):
            return self._reply(h, 401, {"success": False, "code": "UNAUTHORIZED", "message": "Invalid token"})

        if path == "/auth/v1/logout":
            self.logout_requests.append(body)
            return self._reply(h, 200, {"success": True})
        if path == "/auth/v1/user/privacy-settings":
            if method != "PATCH":  # the real server only accepts updates here
                return self._reply(h, 405, {"success": False, "code": "BAD_REQUEST", "message": "Method not supported"})
            return self._reply(h, 200, {"success": True, "data": {"lastSeen": "EVERYBODY"}})
        if path == "/msg-history/v1/conversations/web":
            with self.lock:
                failing = self.fail_conversations > 0
                self.fail_conversations -= failing
            if failing:
                return self._reply(h, 503, {"success": False, "code": "SERVICE_UNAVAILABLE", "message": "try again later"})
            return self._reply(h, 200, {"success": True, "data": {"items": self.conversations}})
        if path.startswith("/msg-history/v1/messages/fetch/"):
            chat = path.rsplit("/", 1)[-1]
            items = self.history.get(chat, [])
            size = int(query.get("batchSize", ["30"])[0])
            offset = int(query.get("startMessageId", ["0"])[0])
            page = items[offset : offset + size]
            more = offset + size < len(items)
            return self._reply(h, 200, {"success": True, "data": {"items": page, "page": {"limit": size, "nextCursor": str(offset + size) if more else None}}})
        if path == "/contact/v1/contacts/getcontacts":
            return self._reply(h, 200, {"success": True, "data": {"contacts": self.contacts, "nextCursor": None, "syncedAt": T0}})
        if path == "/auth/v1/user/profiles/sync":
            found = [self.profiles[i] for i in body.get("userIds", []) if i in self.profiles]
            return self._reply(h, 200, {"success": True, "data": {"profiles": found, "syncedAt": T0}})
        if path == "/auth/v1/user/profiles/by-phone":
            found = [p for p in self.profiles.values() if p["phone"] in body.get("phones", [])]
            return self._reply(h, 200, {"success": True, "data": found})
        if path == f"/group/v1/groups/{GROUP}":
            return self._reply(h, 200, {"success": True, "data": self.group})
        if path.startswith("/media/v1/") and path.endswith("/download-url"):
            data = self.files[path.split("/")[3]]
            chunks = [
                {"chunkIndex": 1, "downloadUrl": f"{self.api_base}/blob/file-1", "rangeStart": 16, "rangeEnd": len(data) - 1},
                {"chunkIndex": 0, "downloadUrl": f"{self.api_base}/blob/file-1", "rangeStart": 0, "rangeEnd": 15},
            ]
            return self._reply(h, 200, {"success": True, "data": {"fileSizeBytes": len(data), "mimeType": "image/jpeg", "chunkDownloads": chunks}})
        if path == "/media/v1/upload/initiate":
            sid = f"up-{len(self.uploads) + 1}"
            count = max(1, math.ceil(body["fileSizeBytes"] / 8))
            with self.lock:
                self.uploads[sid] = {"request": body, "chunks": {}}
            urls = [{"chunkIndex": i, "uploadUrl": f"{self.api_base}/put/{sid}/{i}", "objectKey": f"k/{i}"} for i in range(count)]
            return self._reply(h, 200, {"success": True, "data": {"sessionId": sid, "chunkSizeBytes": 8, "chunkUrls": urls}})
        if path.startswith("/media/v1/upload/") and path.endswith("/complete"):
            sid = path.split("/")[4]
            upload = self.uploads[sid]
            expected = [f'"etag-{i}"' for i in range(len(upload["chunks"]))]
            if body.get("eTags") != expected:
                return self._reply(h, 400, {"success": False, "message": "bad etags"})
            file_id = f"file-{sid}"
            self.files[file_id] = b"".join(upload["chunks"][i] for i in range(len(expected)))
            return self._reply(h, 200, {"success": True, "data": {"fileId": file_id}})
        return self._reply(h, 404, {"success": False, "message": f"no route {path}"})

    def _login_payload(self):
        return {"accessToken": self.access_token, "refreshToken": self.refresh_token, "userId": int(USER), "profile": {"userId": USER, "name": "Me", "phone": "+919800000001"}}

    # ---- QR login socket ------------------------------------------------------------------

    def _qr_handler(self, ws):
        query = parse_qs(urlparse(ws.request.path).query)
        if query.get("appId") != ["hike-messenger"] or not query.get("webDeviceId"):
            ws.close(4003)
            return
        self.qr_device_ids.append(query["webDeviceId"][0])
        ws.send(json.dumps({"type": "QR_GENERATED", "qrToken": "qr-token-abc", "expiresInSeconds": 120, "appId": "hike-messenger"}))
        time.sleep(0.3)
        ws.send(json.dumps({"type": "LOGIN_CONFIRMED", **self._login_payload()}))
        try:
            for _ in ws:
                pass
        except Exception:
            pass

    # ---- MQTT relay ----------------------------------------------------------------------------

    @staticmethod
    def _frame(first: int, body: bytes) -> bytes:
        n, out = len(body), bytearray()
        while True:
            byte, n = n % 128, n // 128
            out.append(byte | (0x80 if n else 0))
            if not n:
                break
        return bytes([first]) + bytes(out) + body

    def _mqtt_handler(self, ws):
        session = {"ws": ws, "topics": set(), "user": None}
        buf = bytearray()
        try:
            for frame in ws:
                buf += frame if isinstance(frame, bytes) else frame.encode()
                while len(buf) >= 2:
                    length, mult, pos = 0, 1, 1
                    while True:
                        if pos >= len(buf):
                            break
                        byte = buf[pos]
                        length += (byte & 0x7F) * mult
                        pos += 1
                        if not byte & 0x80:
                            break
                        mult *= 128
                    if pos + length > len(buf):
                        break
                    first, body = buf[0], bytes(buf[pos : pos + length])
                    del buf[: pos + length]
                    if not self._mqtt_packet(session, first, body):
                        return
        except Exception:
            pass
        finally:
            with self.lock:
                if session in self.sessions:
                    self.sessions.remove(session)
                if session.pop("open", False):
                    self.open_relays -= 1

    def _mqtt_packet(self, session, first: int, body: bytes) -> bool:
        ws, ptype = session["ws"], first >> 4
        if ptype == 1:  # CONNECT
            pos = 2 + struct.unpack_from("!H", body, 0)[0]
            level, flags = body[pos], body[pos + 1]
            keepalive = struct.unpack_from("!H", body, pos + 2)[0]
            pos += 4
            fields = []
            while pos < len(body):
                n = struct.unpack_from("!H", body, pos)[0]
                fields.append(body[pos + 2 : pos + 2 + n].decode())
                pos += 2 + n
            client_id, username, password = (fields + [None, None])[:3]
            ok = self.access_valid and username == USER and password == self.access_token
            with self.lock:
                self.mqtt_connects.append({"client_id": client_id, "username": username, "level": level, "flags": flags, "keepalive": keepalive, "accepted": ok})
            ws.send(self._frame(0x20, bytes([0, 0 if ok else 4])))
            if not ok:
                ws.close()
                return False
            session["user"] = username
            with self.lock:
                session["open"] = True
                self.open_relays += 1
                self.peak_open_relays = max(self.peak_open_relays, self.open_relays)
        elif ptype == 8:  # SUBSCRIBE
            pid = struct.unpack_from("!H", body, 0)[0]
            n = struct.unpack_from("!H", body, 2)[0]
            topic = body[4 : 4 + n].decode()
            session["topics"].add(topic)
            with self.lock:
                if session not in self.sessions:
                    self.sessions.append(session)
            ws.send(self._frame(0x90, struct.pack("!H", pid) + bytes([body[4 + n]])))
        elif ptype == 3:  # PUBLISH
            qos = (first >> 1) & 3
            n = struct.unpack_from("!H", body, 0)[0]
            topic = body[2 : 2 + n].decode()
            pos = 2 + n
            pid = None
            if qos:
                pid = struct.unpack_from("!H", body, pos)[0]
                pos += 2
            packet = json.loads(body[pos:])
            with self.lock:
                self.published.append((topic, packet))
            ack = b""
            if packet.get("t") == "m":
                self.seq += 1
                server_id = f"{self.seq}:{packet['ts']}".encode()
                ack = struct.pack("!H", len(server_id)) + server_id
                echo = dict(packet, s=server_id.decode())
                threading.Thread(target=self.push, args=(echo,), daemon=True).start()
                time.sleep(self.puback_delay)
            if qos == 1:
                ws.send(self._frame(0x40, struct.pack("!H", pid) + ack))
        elif ptype == 12:  # PINGREQ
            ws.send(self._frame(0xD0, b""))
        elif ptype == 14:  # DISCONNECT
            ws.close()
            return False
        return True
