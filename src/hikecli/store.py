"""Local store layout, cross-process file locks, and the saved login session."""

from __future__ import annotations

import json
import os
import time
import uuid
from dataclasses import asdict, dataclass, field
from pathlib import Path


def default_root() -> Path:
    env = os.environ.get("HIKECLI_STORE")
    if env:
        return Path(env).expanduser()
    return Path.home() / ".hikecli"


class Store:
    """One store = one Hike account: session.json, hike.db, media/, lock files."""

    def __init__(self, root: str | os.PathLike | None = None):
        self.root = Path(root).expanduser() if root else default_root()

    @property
    def db_path(self) -> Path:
        return self.root / "hike.db"

    @property
    def session_path(self) -> Path:
        return self.root / "session.json"

    @property
    def device_path(self) -> Path:
        return self.root / "device.json"

    @property
    def media_dir(self) -> Path:
        return self.root / "media"

    @property
    def lock_path(self) -> Path:
        return self.root / "LOCK"

    @property
    def refresh_lock_path(self) -> Path:
        return self.root / "refresh.lock"

    @property
    def qr_png_path(self) -> Path:
        return self.root / "login-qr.png"

    def ensure(self) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        if os.name != "nt":
            try:
                os.chmod(self.root, 0o700)
            except OSError:
                pass


class LockHeld(Exception):
    pass


class FileLock:
    """Exclusive cross-process lock (msvcrt on Windows, flock elsewhere).

    On Windows the locked byte sits far past the pid text, so other processes can
    still read which pid holds the lock.
    """

    _WIN_OFFSET = 1 << 20

    def __init__(self, path: str | os.PathLike):
        self.path = Path(path)
        self._fh = None

    def acquire(self, timeout: float | None = 0.0) -> bool:
        deadline = None if timeout is None else time.monotonic() + timeout
        self.path.parent.mkdir(parents=True, exist_ok=True)
        fh = open(self.path, "a+b")
        while True:
            try:
                self._lock(fh)
                break
            except OSError:
                if deadline is not None and time.monotonic() >= deadline:
                    fh.close()
                    return False
                time.sleep(0.1)
        self._fh = fh
        try:
            fh.seek(0)
            fh.truncate()
            fh.write(f"{os.getpid()}\n".encode())
            fh.flush()
        except OSError:
            pass
        return True

    def release(self) -> None:
        fh, self._fh = self._fh, None
        if fh is None:
            return
        try:
            if os.name == "nt":
                import msvcrt

                fh.seek(self._WIN_OFFSET)
                msvcrt.locking(fh.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                import fcntl

                fcntl.flock(fh.fileno(), fcntl.LOCK_UN)
        except OSError:
            pass
        finally:
            fh.close()

    def holder_pid(self) -> int | None:
        try:
            text = self.path.read_text(encoding="ascii", errors="ignore").strip()
            return int(text.split()[0]) if text else None
        except (OSError, ValueError, IndexError):
            return None

    def _lock(self, fh) -> None:
        if os.name == "nt":
            import msvcrt

            fh.seek(self._WIN_OFFSET)
            msvcrt.locking(fh.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl

            fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)

    def __enter__(self) -> "FileLock":
        if not self.acquire(timeout=None):
            raise LockHeld(str(self.path))
        return self

    def __exit__(self, *exc) -> None:
        self.release()


def now_ms() -> int:
    return int(time.time() * 1000)


def _atomic_write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f"{path.name}.{os.getpid()}.tmp")
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(data, fh, indent=2, ensure_ascii=False)
        fh.flush()
        os.fsync(fh.fileno())
    if os.name != "nt":
        os.chmod(tmp, 0o600)
    # Windows refuses to replace a file another process has open; readers only hold it briefly.
    for attempt in range(20):
        try:
            os.replace(tmp, path)
            return
        except PermissionError:
            if attempt == 19:
                raise
            time.sleep(0.05)


@dataclass
class Session:
    user_id: str
    access_token: str
    refresh_token: str
    profile: dict = field(default_factory=dict)
    login_method: str = "qr"
    created_at: int = 0
    refreshed_at: int = 0
    revoked: str | None = None

    @property
    def display_name(self) -> str:
        return self.profile.get("name") or self.profile.get("phone") or self.user_id


def load_session(store: Store) -> Session | None:
    try:
        raw = json.loads(store.session_path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    except (OSError, ValueError):
        return None
    if not isinstance(raw, dict) or not raw.get("user_id") or not raw.get("access_token"):
        return None
    known = {k: raw[k] for k in Session.__dataclass_fields__ if k in raw}
    known["user_id"] = str(known["user_id"])
    return Session(**known)


def save_session(store: Store, session: Session) -> None:
    store.ensure()
    _atomic_write_json(store.session_path, asdict(session))


def delete_session(store: Store) -> bool:
    try:
        store.session_path.unlink()
        return True
    except FileNotFoundError:
        return False


def device_id(store: Store) -> str:
    """Stable per-store web device id (Hike Web keeps it in localStorage as hike_device_id)."""
    try:
        raw = json.loads(store.device_path.read_text(encoding="utf-8"))
        if isinstance(raw, dict) and raw.get("deviceId"):
            return str(raw["deviceId"])
    except (OSError, ValueError):
        pass
    value = str(uuid.uuid4())
    store.ensure()
    _atomic_write_json(store.device_path, {"deviceId": value})
    return value
