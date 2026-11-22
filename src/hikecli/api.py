"""Hike REST API client (https://api-ext.hikeapp.com), mirroring Hike Web's fetch wrapper.

Hike Web sends the raw access token in the Authorization header (no "Bearer"),
plus X-User-Id, X-App-Id, X-App-Version and X-Build-Number. A 401 triggers one
POST /auth/v1/refresh and a retry.
"""

from __future__ import annotations

import json
from urllib.parse import quote

import requests

from .config import APP_ID, USER_AGENT, api_base, app_version, build_number, device_description
from .store import FileLock, Session, Store, load_session, now_ms, save_session


class HikeError(Exception):
    def __init__(self, message: str, *, status: int | None = None, code: str | None = None, data=None):
        super().__init__(message)
        self.status = status
        self.code = code
        self.data = data


class AuthRequired(HikeError):
    """No usable login: never logged in, logged out, or the refresh token was rejected."""


class NetworkError(HikeError):
    pass


def _seg(value: str) -> str:
    return quote(str(value), safe="")


class HikeAPI:
    def __init__(self, store: Store, session: Session | None = None, *, timeout: float = 30.0):
        self.store = store
        self.session = session
        self.timeout = timeout
        self.http = requests.Session()

    # ---- transport -------------------------------------------------------

    def headers(self, auth: bool = True) -> dict:
        h = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "X-App-Id": APP_ID,
            "X-App-Version": app_version(),
            "X-Build-Number": build_number(),
            "User-Agent": USER_AGENT,
        }
        if auth and self.session:
            h["Authorization"] = self.session.access_token
            h["X-User-Id"] = self.session.user_id
        return h

    def _send(self, method: str, path: str, body=None, params=None, auth: bool = True):
        url = f"{api_base()}{path}"
        try:
            resp = self.http.request(
                method,
                url,
                headers=self.headers(auth),
                data=json.dumps(body) if body is not None else None,
                params=params,
                timeout=self.timeout,
            )
        except requests.RequestException as exc:
            raise NetworkError(f"network error talking to Hike ({method} {path}): {exc}") from exc
        payload = None
        if resp.text.strip():
            try:
                payload = resp.json()
            except ValueError:
                payload = None
        if not resp.ok:
            message, code, data = f"HTTP {resp.status_code}", None, None
            if isinstance(payload, dict):
                if isinstance(payload.get("message"), str):
                    message = payload["message"]
                code = payload.get("code") if isinstance(payload.get("code"), str) else None
                data = payload.get("data")
            raise HikeError(f"{method} {path} failed: {message}", status=resp.status_code, code=code, data=data)
        return payload

    def request(self, method: str, path: str, body=None, params=None, auth: bool = True):
        if auth and not self.session:
            raise AuthRequired("not logged in; run: hikecli auth")
        try:
            return self._send(method, path, body, params, auth)
        except HikeError as exc:
            if exc.status != 401 or not auth:
                raise
        self.refresh()
        return self._send(method, path, body, params, auth)

    def refresh(self) -> None:
        """Rotate the access token. Serialized across processes so two hikecli
        processes never spend the same refresh token."""
        if not self.session:
            raise AuthRequired("not logged in; run: hikecli auth")
        stale = self.session.access_token
        lock = FileLock(self.store.refresh_lock_path)
        if not lock.acquire(timeout=30):
            raise HikeError("timed out waiting for another hikecli process to refresh the login")
        try:
            current = load_session(self.store)
            if current is None:
                raise AuthRequired("logged out; run: hikecli auth")
            self.session = current
            if current.revoked:
                raise AuthRequired(f"saved login is no longer valid ({current.revoked}); run: hikecli auth")
            if current.access_token != stale:
                return  # another process already refreshed
            try:
                payload = self._send(
                    "POST",
                    "/auth/v1/refresh",
                    {"refreshToken": current.refresh_token, "deviceType": "WEB"},
                    auth=True,
                )
            except HikeError as exc:
                if exc.status in (401, 403):
                    current.revoked = f"refresh rejected with HTTP {exc.status}"
                    save_session(self.store, current)
                    raise AuthRequired(
                        "Hike rejected the saved login (the web device may have been logged out "
                        "from the phone). Run: hikecli auth"
                    ) from exc
                raise
            data = (payload or {}).get("data") or {}
            if not data.get("accessToken"):
                raise HikeError("token refresh response did not include an access token")
            current.access_token = data["accessToken"]
            if data.get("refreshToken"):
                current.refresh_token = data["refreshToken"]
            current.refreshed_at = now_ms()
            save_session(self.store, current)
        finally:
            lock.release()

    # ---- auth ------------------------------------------------------------

    def otp_initiate(self, country_code: str, mobile_number: str, device_id: str) -> dict:
        return self._send(
            "POST",
            "/auth/v1/initiate",
            {"mobileNumber": mobile_number, "countryCode": country_code, "deviceType": "WEB", "deviceFingerprint": device_id},
            auth=False,
        ) or {}

    def otp_verify(self, challenge_id: str, otp: str, device_id: str) -> dict:
        return self._send(
            "POST",
            "/auth/v1/verify",
            {
                "challengeId": challenge_id,
                "otp": otp,
                "deviceType": "WEB",
                "deviceId": device_id,
                "name": "",
                "deviceOsVersion": device_description(),
            },
            auth=False,
        ) or {}

    def logout(self, refresh_token: str) -> None:
        self._send("POST", "/auth/v1/logout", {"refreshToken": refresh_token, "logoutAllDevices": False}, auth=True)

    def privacy_settings(self) -> dict:
        return (self.request("GET", "/auth/v1/user/privacy-settings") or {}).get("data") or {}

    # ---- conversations and messages ---------------------------------------

    def conversations(self) -> list[dict]:
        payload = self.request("GET", "/msg-history/v1/conversations/web") or {}
        return (payload.get("data") or {}).get("items") or []

    def messages(self, conversation_id: str, batch_size: int = 50, cursor: str | None = None) -> tuple[list[dict], str | None]:
        params = {"batchSize": str(batch_size)}
        if cursor:
            params["startMessageId"] = cursor
        payload = self.request("GET", f"/msg-history/v1/messages/fetch/{_seg(conversation_id)}", params=params) or {}
        data = payload.get("data") or {}
        items = data.get("items") or []
        page = data.get("page") or {}
        limit = page.get("limit") or 0
        next_cursor = page.get("nextCursor") if items and len(items) >= limit else None
        return items, next_cursor

    # ---- people and groups -----------------------------------------------

    def contacts_page(self, synced_at: int = 0, next_cursor: str | None = None, batch_size: int = 200):
        params = {"batchSize": str(batch_size), "syncedAt": str(synced_at)}
        if next_cursor:
            params["nextCursor"] = next_cursor
        data = (self.request("GET", "/contact/v1/contacts/getcontacts", params=params) or {}).get("data") or {}
        return data.get("contacts") or [], data.get("nextCursor"), data.get("syncedAt")

    def profiles(self, user_ids: list[str]) -> list[dict]:
        body = {"userIds": user_ids, "newUserIds": user_ids, "syncedAt": 0}
        data = (self.request("POST", "/auth/v1/user/profiles/sync", body) or {}).get("data") or {}
        return data.get("profiles") or []

    def profiles_by_phone(self, phones: list[str]) -> list[dict]:
        data = (self.request("POST", "/auth/v1/user/profiles/by-phone", {"phones": phones, "appId": APP_ID}) or {}).get("data")
        return data if isinstance(data, list) else []

    def group(self, group_id: str) -> dict:
        return (self.request("GET", f"/group/v1/groups/{_seg(group_id)}") or {}).get("data") or {}

    # ---- media -------------------------------------------------------------

    def media_download_info(self, file_id: str) -> dict:
        return (self.request("GET", f"/media/v1/{_seg(file_id)}/download-url") or {}).get("data") or {}

    def upload_initiate(self, file_name: str, mime_type: str, size: int) -> dict:
        body = {"fileName": file_name, "mimeType": mime_type, "fileSizeBytes": size}
        return (self.request("POST", "/media/v1/upload/initiate", body) or {}).get("data") or {}

    def upload_complete(self, session_id: str, etags: list[str]) -> str:
        data = (self.request("POST", f"/media/v1/upload/{_seg(session_id)}/complete", {"eTags": etags}) or {}).get("data") or {}
        if not data.get("fileId"):
            raise HikeError("upload completion did not return a fileId")
        return str(data["fileId"])
