"""Endpoints and client identity used by Hike Web (web.hikeapp.com).

The values come from the config object in the Hike Web bundle (apiBase, qrWs, mqtt,
appVersion, buildNumber). Each one can be overridden with an environment variable,
which is how the tests point the CLI at a local fake server.
"""

from __future__ import annotations

import os
import platform

from . import __version__

APP_ID = "hike-messenger"
WEB_ORIGIN = "https://web.hikeapp.com"
USER_AGENT = f"hikecli/{__version__}"


def _env(name: str, default: str) -> str:
    value = os.environ.get(name)
    return value if value else default


def api_base() -> str:
    return _env("HIKECLI_API_BASE", "https://api-ext.hikeapp.com").rstrip("/")


def session_ws_base() -> str:
    return _env("HIKECLI_SESSION_WS", "wss://session.hikeapp.com").rstrip("/")


def mqtt_ws_url() -> str:
    return _env("HIKECLI_MQTT_WS", "wss://relay-ws.hikeapp.com:443/mqtt")


def app_version() -> str:
    return _env("HIKECLI_APP_VERSION", "1.3.2")


def build_number() -> str:
    return _env("HIKECLI_BUILD_NUMBER", "39")


def device_description() -> str:
    """Sent as deviceOsVersion during OTP login (Hike Web sends navigator.userAgent)."""
    return f"{USER_AGENT} ({platform.system()} {platform.release()})"
