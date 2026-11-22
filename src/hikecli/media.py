"""Media transfer. Hike stores media unencrypted behind signed storage URLs.

Download: GET /media/v1/{fileId}/download-url -> byte-range chunks.
Upload: POST /media/v1/upload/initiate -> PUT each chunk to a signed URL (keep the
ETag) -> POST /media/v1/upload/{sessionId}/complete -> fileId.
The signed URLs belong to a storage provider, so the Hike login token is never sent there.
"""

from __future__ import annotations

import mimetypes
import os
from pathlib import Path

import requests

from .api import HikeAPI, HikeError
from .config import USER_AGENT

MAX_UPLOAD_BYTES = 1_000_000_000  # Hike Web's limit (1 GB)


def guess_mime(path: Path) -> str:
    return mimetypes.guess_type(path.name)[0] or "application/octet-stream"


def download(api: HikeAPI, file_id: str, dest: Path, *, timeout: float = 120.0) -> dict:
    info = api.media_download_info(file_id)
    chunks = sorted(info.get("chunkDownloads") or [], key=lambda c: c.get("chunkIndex", 0))
    if not chunks:
        raise HikeError(f"Hike returned no download chunks for file {file_id}")
    total = info.get("fileSizeBytes")
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_name(dest.name + ".part")
    written = 0
    try:
        with requests.Session() as http, open(tmp, "wb") as fh:
            for chunk in chunks:
                start, end = int(chunk["rangeStart"]), int(chunk["rangeEnd"])
                resp = http.get(
                    chunk["downloadUrl"],
                    headers={"Range": f"bytes={start}-{end}", "User-Agent": USER_AGENT},
                    stream=True,
                    timeout=timeout,
                )
                if resp.status_code not in (200, 206):
                    raise HikeError(f"media chunk {chunk.get('chunkIndex')} download failed: HTTP {resp.status_code}")
                got = 0
                for block in resp.iter_content(65536):
                    fh.write(block)
                    got += len(block)
                written += got
                if resp.status_code == 200:
                    break  # the server ignored Range and sent the whole file
                if got != end - start + 1:
                    raise HikeError(f"media chunk {chunk.get('chunkIndex')} was truncated ({got} of {end - start + 1} bytes)")
        if total is not None and written != int(total):
            raise HikeError(f"downloaded {written} bytes but Hike reported {total}")
        os.replace(tmp, dest)
    except BaseException:
        tmp.unlink(missing_ok=True)
        raise
    return {"path": str(dest), "size": written, "mime": info.get("mimeType")}


def upload(api: HikeAPI, path: Path, mime: str, *, timeout: float = 300.0) -> str:
    size = path.stat().st_size
    if size > MAX_UPLOAD_BYTES:
        raise HikeError(f"{path.name} is larger than Hike's 1 GB limit")
    session = api.upload_initiate(path.name, mime, size)
    chunk_size = int(session.get("chunkSizeBytes") or 0)
    parts = sorted(session.get("chunkUrls") or [], key=lambda c: c["chunkIndex"])
    if not parts or chunk_size <= 0:
        raise HikeError("Hike did not return upload URLs")
    etags: list[str | None] = [None] * (max(p["chunkIndex"] for p in parts) + 1)
    with requests.Session() as http, open(path, "rb") as fh:
        for part in parts:
            index = int(part["chunkIndex"])
            start = index * chunk_size
            fh.seek(start)
            data = fh.read(max(0, min(start + chunk_size, size) - start))
            resp = http.put(part["uploadUrl"], data=data, headers={"Content-Type": mime}, timeout=timeout)
            if not 200 <= resp.status_code < 300:
                raise HikeError(f"upload of chunk {index} failed: HTTP {resp.status_code}")
            etag = resp.headers.get("ETag")
            if not etag:
                raise HikeError(f"upload of chunk {index} returned no ETag")
            etags[index] = etag
    if any(tag is None for tag in etags):
        raise HikeError("Hike's upload plan skipped a chunk index")
    return api.upload_complete(str(session["sessionId"]), etags)
