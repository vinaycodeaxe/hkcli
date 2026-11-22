"""Login flows used by Hike Web.

QR: open wss://session.hikeapp.com/session/v1/qr-login?webDeviceId=..&appId=hike-messenger.
The server sends {"type":"QR_GENERATED","qrToken":..,"expiresInSeconds":..}; the QR
encodes qrToken as-is. Scanning it in the Hike phone app (Settings > Web QR Login)
makes the server send {"type":"LOGIN_CONFIRMED","accessToken","refreshToken","userId","profile"}.
The client pings every 20 s and asks for a fresh QR 20 s before expiry.

OTP: POST /auth/v1/initiate then POST /auth/v1/verify with deviceType WEB.
"""

from __future__ import annotations

import json
import os
import struct
import sys
import time
from typing import Callable
from urllib.parse import quote

import segno
import websocket

from .api import HikeAPI, HikeError
from .config import APP_ID, USER_AGENT, WEB_ORIGIN, session_ws_base

PING_SECONDS = 20
REFRESH_BEFORE_SECONDS = 20
FATAL_CLOSE_CODES = {1008, 4001, 4003}


class LoginError(Exception):
    pass


def qr_login_url(device_id: str) -> str:
    return f"{session_ws_base()}/session/v1/qr-login?webDeviceId={quote(device_id, safe='')}&appId={quote(APP_ID, safe='')}"


def qr_login(
    device_id: str,
    *,
    on_qr: Callable[[str, int], None],
    on_status: Callable[[str], None],
    timeout: float = 300.0,
) -> dict:
    """Run the QR handshake until the phone confirms; returns the LOGIN_CONFIRMED payload."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            ws = websocket.create_connection(
                qr_login_url(device_id), timeout=15, origin=WEB_ORIGIN, header=[f"User-Agent: {USER_AGENT}"]
            )
        except Exception as exc:
            on_status(f"cannot reach the Hike login server ({exc}); retrying")
            time.sleep(2)
            continue
        try:
            result = _qr_session(ws, deadline, on_qr, on_status)
        finally:
            try:
                ws.shutdown()
            except Exception:
                pass
        if result is not None:
            return result
        if deadline - time.monotonic() > 2:
            time.sleep(2)
    raise LoginError("timed out waiting for the QR code to be scanned")


def _qr_session(ws, deadline: float, on_qr, on_status) -> dict | None:
    ws.settimeout(1.0)
    last_ping = time.monotonic()
    expires_at = None
    refresh_sent = False
    while time.monotonic() < deadline:
        now = time.monotonic()
        try:
            if now - last_ping >= PING_SECONDS:
                ws.send(json.dumps({"type": "PING"}))
                last_ping = now
            if expires_at is not None and not refresh_sent and expires_at - now <= REFRESH_BEFORE_SECONDS:
                ws.send(json.dumps({"type": "REFRESH_QR"}))
                refresh_sent = True
            opcode, data = ws.recv_data(control_frame=True)
        except (websocket.WebSocketTimeoutException, TimeoutError):
            continue
        except Exception as exc:
            on_status(f"login connection dropped ({exc}); reconnecting")
            return None
        if opcode == websocket.ABNF.OPCODE_CLOSE:
            code = struct.unpack("!H", data[:2])[0] if data and len(data) >= 2 else None
            if code in FATAL_CLOSE_CODES:
                raise LoginError(f"Hike refused the login session (close code {code})")
            on_status(f"login server closed the connection (code {code}); reconnecting")
            return None
        if opcode not in (websocket.ABNF.OPCODE_TEXT, websocket.ABNF.OPCODE_BINARY):
            continue
        try:
            msg = json.loads(data)
        except ValueError:
            continue
        kind = msg.get("type") if isinstance(msg, dict) else None
        if kind == "QR_GENERATED" and msg.get("qrToken"):
            seconds = int(msg.get("expiresInSeconds") or 120)
            expires_at = time.monotonic() + seconds
            refresh_sent = False
            on_qr(str(msg["qrToken"]), seconds)
        elif kind == "QR_EXPIRED":
            on_status("QR code expired; asking for a new one")
            ws.send(json.dumps({"type": "REFRESH_QR"}))
            expires_at, refresh_sent = None, True
        elif kind == "LOGIN_CONFIRMED":
            if not (msg.get("accessToken") and msg.get("refreshToken") and msg.get("userId") is not None):
                raise LoginError("Hike confirmed the login but did not send tokens")
            return msg
        elif kind == "LOGIN_FAILED":
            raise LoginError(msg.get("message") or "Hike rejected the login")
    return None


def otp_login(api: HikeAPI, device_id: str, country_code: str, mobile: str, *, read_otp: Callable[[int], str]) -> dict:
    started = api.otp_initiate(country_code, mobile, device_id)
    if not started.get("success", True) or not (started.get("data") or {}).get("challengeId"):
        raise LoginError(started.get("message") or "Hike could not send the OTP")
    data = started["data"]
    for attempt in range(3):
        code = read_otp(int(data.get("expiresInSeconds") or 0)).strip()
        if not code:
            raise LoginError("no OTP entered")
        try:
            verified = api.otp_verify(str(data["challengeId"]), code, device_id)
        except HikeError as exc:
            if exc.status is not None and exc.status < 500 and attempt < 2:
                print(f"{exc}; try again", file=sys.stderr)
                continue
            raise LoginError(str(exc)) from exc
        if verified.get("success", True) and (verified.get("data") or {}).get("accessToken"):
            return verified["data"]
        if attempt == 2:
            raise LoginError(verified.get("message") or "invalid OTP")
        print(f"{verified.get('message') or 'invalid OTP'}; try again", file=sys.stderr)
    raise LoginError("invalid OTP")


# ---- QR rendering ----------------------------------------------------------------


def enable_ansi(stream=None) -> bool:
    """Turn on ANSI colours for the console behind `stream` (default stderr, where the QR goes)."""
    stream = stream or sys.stderr
    if not stream.isatty():
        return False
    if os.name != "nt":
        return True
    try:
        import ctypes
        from ctypes import wintypes

        kernel32 = ctypes.windll.kernel32
        handle = kernel32.GetStdHandle(-12 if stream is sys.stderr else -11)
        mode = wintypes.DWORD()
        if not kernel32.GetConsoleMode(handle, ctypes.byref(mode)):
            return False
        return bool(mode.value & 0x0004) or bool(kernel32.SetConsoleMode(handle, mode.value | 0x0004))
    except Exception:
        return False


def render_qr(token: str, *, ansi: bool) -> str:
    """Half-block QR. With ANSI it forces black-on-white so it scans on dark terminals too."""
    rows = [list(row) for row in segno.make(token, error="h", micro=False).matrix_iter(scale=1, border=2)]
    if len(rows) % 2:
        rows.append([0] * len(rows[0]))
    lines = []
    for top, bottom in zip(rows[0::2], rows[1::2]):
        if ansi:
            cells = ("█" if t and b else "▀" if t else "▄" if b else " " for t, b in zip(top, bottom))
            lines.append("\x1b[30;107m" + "".join(cells) + "\x1b[0m")
        else:
            # Without colours, draw light modules so the code reads correctly on a dark background.
            cells = (" " if t and b else "▄" if t else "▀" if b else "█" for t, b in zip(top, bottom))
            lines.append("".join(cells))
    return "\n".join(lines)


def save_qr_png(token: str, path) -> None:
    segno.make(token, error="h", micro=False).save(str(path), scale=8, border=4)
