from __future__ import annotations

import asyncio
import base64
import hashlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from urllib.parse import parse_qs, urlsplit

import pytest

from content_agent_sidecar.backend import BackendClient, BackendError


@pytest.mark.parametrize("method", ["image", "video", "frames"])
@pytest.mark.parametrize("failure", [
    "none", "identity", "snapshot", "deleted", "checksum", "size_bool", "size_zero",
    "oversize", "changed_bytes", "wrong_mime", "snapshot_race", "expired",
])
def test_media_bytes_are_bound_to_exact_snapshot_before_use(method, failure, monkeypatch):
    data = b"immutable-media-fixture"
    metadata = {
        "asset_id": "asset 1", "current_snapshot_id": "snapshot 1", "project_id": "project-1",
        "kind": "image" if method == "image" else "video", "status": "available",
        "size_bytes": len(data), "checksum_algorithm": "sha256",
        "checksum": hashlib.sha256(data).hexdigest(),
    }
    changes = {
        "identity": {"asset_id": "foreign"}, "snapshot": {"current_snapshot_id": "changed"},
        "deleted": {"deleted_at": "deleted"}, "checksum": {"checksum": "invalid"},
        "size_bool": {"size_bytes": True}, "size_zero": {"size_bytes": 0},
        "oversize": {"size_bytes": 501 * 1024 * 1024},
    }
    metadata.update(changes.get(failure, {}))
    requests, extracted = [], []
    mime = "image/png" if method == "image" else "video/mp4"

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            requests.append(self.path)
            path = urlsplit(self.path)
            if path.path.endswith("/content"):
                assert path.path == "/api/v1/assets/asset%201/content"
                assert parse_qs(path.query) == {"asset_snapshot_id": ["snapshot 1"]}
                self.send_response(409 if failure == "snapshot_race" else 410 if failure == "expired" else 200)
                self.send_header("Content-Type", "text/html" if failure == "wrong_mime" else mime)
                self.end_headers()
                self.wfile.write(b"x" * len(data) if failure == "changed_bytes" else data)
            else:
                assert path.path == "/api/v1/assets/asset%201"
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps({"data": metadata}).encode())

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = Thread(target=server.serve_forever, daemon=True)
    thread.start()
    client = BackendClient(f"http://127.0.0.1:{server.server_port}", internal_token="isolated-fixture")

    def extract(content):
        extracted.append(content)
        return ["data:image/jpeg;base64,ZnJhbWU="]

    monkeypatch.setattr(client, "_extract_video_frames_sync", extract)
    operation = {"image": client.get_image_data_url, "video": client.get_video_content,
                 "frames": client.get_video_frame_data_urls}[method]
    try:
        if failure != "none":
            with pytest.raises(BackendError):
                asyncio.run(operation("asset 1", "snapshot 1"))
            assert not extracted
            if failure in changes:
                assert len(requests) == 1
        else:
            result = asyncio.run(operation("asset 1", "snapshot 1"))
            assert len(requests) == 2
            if method == "image":
                assert result == "data:image/png;base64," + base64.b64encode(data).decode()
            elif method == "video":
                assert result == (data, mime)
            else:
                assert result == ["data:image/jpeg;base64,ZnJhbWU="] and extracted == [data]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
