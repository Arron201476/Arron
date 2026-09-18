from __future__ import annotations

import base64
import binascii
from dataclasses import dataclass
import hashlib
import json
import posixpath
import unicodedata

from .backend import BackendError


MAX_FILE_BYTES = 16 << 20


def portable_file_path(value: str) -> bool:
    if not isinstance(value, str):
        return False
    try:
        size = len(value.encode("utf-8"))
    except UnicodeError:
        return False
    if (not value or size > 240 or value.strip() != value or value.startswith("/") or posixpath.normpath(value) != value
            or any(char in '\\:<>"|?*' or unicodedata.category(char) == "Cc" for char in value)):
        return False
    for part in value.split("/"):
        base = part.split(".", 1)[0].upper()
        if (not part or part.rstrip(". ") != part or base in {"CON", "PRN", "AUX", "NUL"}
                or len(base) == 4 and base[:3] in {"COM", "LPT"} and base[3] in "123456789"):
            return False
    return True


def _go_json_hash(value: dict) -> str:
    encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    for plain, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        encoded = encoded.replace(plain, escaped)
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class NativeFileOperation:
    operation: str
    path: str
    data: bytes = b""
    parents: bool = False
    recursive: bool = False
    mode: int | None = None

    def payload(self) -> dict:
        if (not isinstance(self.operation, str) or self.operation not in {"read", "stat", "list", "write", "mkdir", "remove", "chmod"}
                or not isinstance(self.path, str) or self.path != "." and not portable_file_path(self.path)
                or self.path == "." and self.operation not in {"stat", "list", "mkdir"}
                or type(self.parents) is not bool or type(self.recursive) is not bool
                or self.parents and self.operation != "mkdir" or self.recursive and self.operation != "remove"
                or self.mode is not None and (self.operation != "chmod" or type(self.mode) is not int or not 0 <= self.mode <= 0o777)
                or self.operation == "chmod" and self.mode is None
                or not isinstance(self.data, bytes) or len(self.data) > MAX_FILE_BYTES
                or self.operation != "write" and self.data):
            raise BackendError("File operation is outside the bounded native filesystem contract")
        # Match WorkspaceFileOperation's Go field order and omitempty behavior.
        result = {"operation": self.operation, "path": self.path}
        if self.data:
            result["data"] = base64.b64encode(self.data).decode("ascii")
        if self.parents:
            result["parents"] = True
        if self.recursive:
            result["recursive"] = True
        if self.mode is not None:
            result["mode"] = self.mode
        return result

    @property
    def request_hash(self) -> str:
        return _go_json_hash(self.payload())

    @property
    def mutates(self) -> bool:
        return self.operation in {"write", "mkdir", "remove", "chmod"} and not (
            self.operation == "mkdir" and self.path == "." and self.parents
        )


@dataclass(frozen=True)
class NativeFileEntry:
    path: str
    directory: bool
    mode: int
    size_bytes: int
    sha256: str = ""


@dataclass(frozen=True)
class NativeFileResult:
    operation: str
    path: str
    data: bytes
    entries: tuple[NativeFileEntry, ...]
    sha256: str

    @classmethod
    def from_wire(cls, request: NativeFileOperation, value) -> NativeFileResult:
        def invalid():
            return BackendError("File response does not confirm the bounded original operation")

        if (not isinstance(value, dict) or set(value) != {"operation", "path", "data", "entries", "sha256"}
                or value["operation"] != request.operation or value["path"] != request.path
                or not isinstance(value["data"], str) or len(value["data"]) > (MAX_FILE_BYTES + 2) // 3 * 4
                or not isinstance(value["entries"], list) or len(value["entries"]) > 1024):
            raise invalid()
        data = None
        try:
            data = base64.b64decode(value["data"], validate=True)
        except (ValueError, binascii.Error):
            pass
        if data is None or len(data) > MAX_FILE_BYTES or base64.b64encode(data).decode("ascii") != value["data"]:
            raise invalid()
        entries, seen = [], set()
        for item in value["entries"]:
            if (not isinstance(item, dict) or set(item) - {"path", "directory", "mode", "size_bytes", "sha256"}
                    or not {"path", "directory", "mode", "size_bytes"} <= set(item)
                    or not isinstance(item["path"], str) or item["path"] != "." and not portable_file_path(item["path"])
                    or type(item["directory"]) is not bool or type(item["mode"]) is not int or not 0 <= item["mode"] <= 0o777
                    or type(item["size_bytes"]) is not int or not 0 <= item["size_bytes"] <= MAX_FILE_BYTES
                    or not isinstance(item.get("sha256", ""), str)
                    or item["directory"] and (item["size_bytes"] != 0 or item.get("sha256", ""))
                    or item["path"] == "." and not item["directory"]):
                raise invalid()
            entry = NativeFileEntry(**item)
            if entry.path.lower() in seen:
                raise invalid()
            seen.add(entry.path.lower())
            if request.operation == "list":
                if entry.path == "." or posixpath.dirname(entry.path) != ("" if request.path == "." else request.path):
                    raise invalid()
            elif entry.path != request.path:
                raise invalid()
            entries.append(entry)
        if sum(entry.size_bytes for entry in entries) > MAX_FILE_BYTES or sum(not entry.directory for entry in entries) > 256:
            raise invalid()
        if request.operation in {"read", "write"}:
            body = data if request.operation == "read" else request.data
            if (len(entries) != 1 or entries[0].directory or entries[0].size_bytes != len(body)
                    or value["sha256"] != hashlib.sha256(body).hexdigest() or entries[0].sha256 != value["sha256"]
                    or request.operation == "write" and data):
                raise invalid()
        else:
            if data or value["sha256"] != "" or any(entry.sha256 for entry in entries):
                raise invalid()
            if request.operation in {"stat", "mkdir", "chmod"} and len(entries) != 1:
                raise invalid()
            if request.operation == "mkdir" and not entries[0].directory:
                raise invalid()
            if request.operation == "chmod" and entries[0].mode != request.mode:
                raise invalid()
            if request.operation == "remove" and entries:
                raise invalid()
        return cls(request.operation, request.path, data, tuple(entries), value["sha256"])


SAFE_FILE_ERRORS = frozenset({
    "WORKSPACE_BUSY", "WORKSPACE_ADAPTER_UNAVAILABLE", "WORKSPACE_FILE_REQUEST_INVALID", "WORKSPACE_FILE_PATH_UNSAFE",
    "WORKSPACE_FILE_NOT_FOUND", "WORKSPACE_FILE_EXISTS", "WORKSPACE_FILE_NOT_DIRECTORY", "WORKSPACE_FILE_IS_DIRECTORY",
    "WORKSPACE_FILE_PATH_COLLISION", "WORKSPACE_FILE_LIMIT_EXCEEDED", "WORKSPACE_FILE_ACCESS_DENIED",
    "WORKSPACE_ARCHIVE_ENTRY_UNSAFE", "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "WORKSPACE_ARCHIVE_PATH_UNSAFE", "WORKSPACE_ARCHIVE_PATH_COLLISION",
})
