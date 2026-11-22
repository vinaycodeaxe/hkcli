"""The store lock hkcli takes (LOCK, refresh.lock), so tests can hold it from Python.

Windows: an exclusive lock on one byte at offset 1 MiB, so the pid written at the
start of the file stays readable. Elsewhere: flock on the whole file.
"""

from __future__ import annotations

import os
from pathlib import Path

LOCK_OFFSET = 1 << 20


class FileLock:
    def __init__(self, path: str | os.PathLike):
        self.path = Path(path)
        self._fh = None

    def acquire(self) -> bool:
        """Try once; False if another process holds the lock."""
        self.path.parent.mkdir(parents=True, exist_ok=True)
        fh = open(self.path, "a+b")
        try:
            if os.name == "nt":
                import msvcrt

                fh.seek(LOCK_OFFSET)
                msvcrt.locking(fh.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl

                fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            fh.close()
            return False
        fh.seek(0)
        fh.truncate()
        fh.write(f"{os.getpid()}\n".encode())
        fh.flush()
        self._fh = fh
        return True

    def release(self) -> None:
        fh, self._fh = self._fh, None
        if fh is None:
            return
        try:
            if os.name == "nt":
                import msvcrt

                fh.seek(LOCK_OFFSET)
                msvcrt.locking(fh.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                import fcntl

                fcntl.flock(fh.fileno(), fcntl.LOCK_UN)
        finally:
            fh.close()
