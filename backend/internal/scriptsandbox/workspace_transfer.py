import contextlib
import base64
import binascii
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import signal
import stat
import sys
import tarfile
import time
import unicodedata
import uuid


class TransferError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def processes():
    result = {}
    for name in os.listdir("/proc"):
        if not name.isdecimal():
            continue
        try:
            fields = Path("/proc", name, "stat").read_text().rsplit(")", 1)[1].split()
            result[int(name)] = (fields[0], fields[19])
        except FileNotFoundError:
            continue
    return result


class LinuxProcess:
    def __init__(self, pid):
        self.fd = os.pidfd_open(pid)

    def send(self, value):
        signal.pidfd_send_signal(self.fd, value)

    def close(self):
        os.close(self.fd)


@contextlib.contextmanager
def quiescent(scan=processes, process_ref=LinuxProcess, own_pid=None, sleep=time.sleep):
    own_pid = os.getpid() if own_pid is None else own_pid
    stopped = {}
    try:
        for _ in range(100):
            current = scan()
            active = [(pid, state) for pid, state in current.items()
                      if pid not in {1, own_pid} and state[0] not in {"T", "t", "Z", "X"}]
            if not active:
                yield
                return
            for pid, state in active:
                try:
                    identity = (pid, state[1])
                    if identity not in stopped:
                        # Keep headroom under the container's nofile=64 policy.
                        if len(stopped) >= 48:
                            raise TransferError("WORKSPACE_BUSY")
                        stopped[identity] = process_ref(pid)
                    stopped[identity].send(signal.SIGSTOP)
                except ProcessLookupError:
                    pass
            sleep(0.01)
        raise TransferError("WORKSPACE_BUSY")
    finally:
        failed = False
        for process in stopped.values():
            try:
                process.send(signal.SIGCONT)
            except ProcessLookupError:
                pass
            except BaseException:
                failed = True
            finally:
                try:
                    process.close()
                except BaseException:
                    failed = True
        if failed:
            raise TransferError("WORKSPACE_TRANSFER_UNCONFIRMED")


def safe_path(name):
    try:
        encoded = name.encode("utf-8")
    except UnicodeError:
        return False
    relative = PurePosixPath(name)
    if (not name or len(encoded) > 240 or relative.is_absolute() or relative.as_posix() != name
            or any(char in name for char in '\\:<>"|?*')
            or any(unicodedata.category(char) == "Cc" for char in name)):
        return False
    for segment in name.split("/"):
        base = segment.split(".", 1)[0].upper()
        if (not segment or segment.rstrip(". ") != segment or base in {"CON", "PRN", "AUX", "NUL"}
                or len(base) == 4 and base[:3] in {"COM", "LPT"} and base[3] in "123456789"):
            return False
    return True


def entries(root, limit, *, hash_contents=True):
    result, total, files = [], 0, 0
    seen = set()
    def fail(error):
        raise error
    for base, directories, names in os.walk(root, followlinks=False, onerror=fail):
        for name in sorted(directories + names):
            target = Path(base, name)
            info = target.lstat()
            directory = stat.S_ISDIR(info.st_mode)
            if not directory and not stat.S_ISREG(info.st_mode):
                raise TransferError("WORKSPACE_ARCHIVE_ENTRY_UNSAFE")
            relative = target.relative_to(root).as_posix()
            if not safe_path(relative):
                raise TransferError("WORKSPACE_ARCHIVE_PATH_UNSAFE")
            if relative.lower() in seen:
                raise TransferError("WORKSPACE_ARCHIVE_PATH_COLLISION")
            seen.add(relative.lower())
            item = {"path": relative, "directory": directory, "mode": stat.S_IMODE(info.st_mode) & 0o777,
                    "size_bytes": 0 if directory else info.st_size}
            if not directory:
                files += 1
                total += info.st_size
                if files > 256 or total > limit:
                    raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
                if hash_contents:
                    digest = hashlib.sha256()
                    with target.open("rb") as source:
                        while chunk := source.read(65536):
                            digest.update(chunk)
                    item["sha256"] = digest.hexdigest()
            result.append(item)
            if len(result) > 1024:
                raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
    return sorted(result, key=lambda item: item["path"].lower())


def export_archive(root, listing, output):
    with tarfile.open(fileobj=output, mode="w|", format=tarfile.PAX_FORMAT) as archive:
        for item in listing:
            header = tarfile.TarInfo(item["path"])
            header.mode = item["mode"]
            header.uid = header.gid = 65532
            header.size = item["size_bytes"]
            if item["directory"]:
                header.type = tarfile.DIRTYPE
                archive.addfile(header)
            else:
                with (root / item["path"]).open("rb") as source:
                    archive.addfile(header, source)


def hydrate_archive(root, data, expected, limit):
    expected = sorted(expected, key=lambda item: item["path"].lower())
    # Validate the entire stream before the first filesystem write, in addition
    # to Go's canonicalization. Never call tarfile.extract/extractall.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
        selected, seen, total, files = [], set(), 0, 0
        for member in archive:
            if not (member.isfile() or member.isdir()) or member.issparse():
                raise TransferError("WORKSPACE_ARCHIVE_ENTRY_UNSAFE")
            if not safe_path(member.name):
                raise TransferError("WORKSPACE_ARCHIVE_PATH_UNSAFE")
            if member.name.lower() in seen:
                raise TransferError("WORKSPACE_ARCHIVE_PATH_COLLISION")
            seen.add(member.name.lower())
            total += member.size
            files += int(member.isfile())
            if (member.size < 0 or member.isdir() and member.size != 0 or total > limit
                    or files > 256 or len(seen) > 1024):
                raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
            item = {"path": member.name, "directory": member.isdir(), "mode": member.mode & 0o777,
                    "size_bytes": member.size}
            if member.isfile():
                with archive.extractfile(member) as source:
                    digest = hashlib.sha256()
                    while chunk := source.read(65536):
                        digest.update(chunk)
                item["sha256"] = digest.hexdigest()
            selected.append(item)
        selected.sort(key=lambda item: item["path"].lower())
        if selected != expected:
            raise TransferError("WORKSPACE_HYDRATE_UNCONFIRMED")
    current = entries(root, limit)
    if current == expected:
        return current
    if current:
        raise TransferError("WORKSPACE_HYDRATE_CONFLICT")
    directories = []
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:") as archive:
        for member in archive:
            relative = PurePosixPath(member.name)
            target = root.joinpath(*relative.parts)
            if member.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
                directories.append((target, member.mode & 0o777))
            else:
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                source = archive.extractfile(member)
                with source, target.open("xb") as destination:
                    while chunk := source.read(65536):
                        destination.write(chunk)
                target.chmod(member.mode & 0o777)
    for target, mode in reversed(directories):
        target.chmod(mode)
    actual = entries(root, limit)
    if actual != expected:
        raise TransferError("WORKSPACE_HYDRATE_UNCONFIRMED")
    return actual


def file_operation(root, request, limit):
    """Run with all other container processes quiescent; never on host paths."""
    allowed = {"operation", "path", "data", "parents", "recursive", "mode"}
    if not isinstance(request, dict) or set(request) - allowed:
        raise TransferError("WORKSPACE_FILE_REQUEST_INVALID")
    operation, name = request.get("operation"), request.get("path")
    parents, recursive, mode = request.get("parents", False), request.get("recursive", False), request.get("mode")
    if (not isinstance(operation, str) or operation not in {"read", "stat", "list", "write", "mkdir", "remove", "chmod"}
            or not isinstance(name, str) or (name != "." and not safe_path(name))
            or type(parents) is not bool or type(recursive) is not bool
            or (parents and operation != "mkdir") or (recursive and operation != "remove")
            or (mode is not None and (operation != "chmod" or type(mode) is not int or not 0 <= mode <= 0o777))
            or (operation == "chmod" and mode is None)
            or name == "." and operation not in {"stat", "list", "mkdir"}):
        raise TransferError("WORKSPACE_FILE_REQUEST_INVALID")
    data = b""
    encoded = request.get("data", "")
    if not isinstance(encoded, str) or len(encoded) > ((16 << 20) + 2) // 3 * 4:
        raise TransferError("WORKSPACE_FILE_REQUEST_INVALID")
    try:
        data = base64.b64decode(encoded, validate=True)
    except (ValueError, binascii.Error):
        raise TransferError("WORKSPACE_FILE_REQUEST_INVALID") from None
    if (base64.b64encode(data).decode("ascii") != encoded or len(data) > min(limit, 16 << 20)
            or operation != "write" and data):
        raise TransferError("WORKSPACE_FILE_REQUEST_INVALID")
    if not stat.S_ISDIR(root.lstat().st_mode):
        raise TransferError("WORKSPACE_FILE_PATH_UNSAFE")
    try:
        listing = entries(root, limit, hash_contents=False)
    except PermissionError:
        raise TransferError("WORKSPACE_FILE_ACCESS_DENIED") from None
    by_name = {item["path"]: item for item in listing}
    by_lower = {item["path"].lower(): item["path"] for item in listing}
    components = [] if name == "." else name.split("/")
    for index in range(1, len(components) + 1):
        prefix = "/".join(components[:index])
        if by_lower.get(prefix.lower(), prefix) != prefix:
            raise TransferError("WORKSPACE_FILE_PATH_COLLISION")
        if index < len(components) and prefix in by_name and not by_name[prefix]["directory"]:
            raise TransferError("WORKSPACE_FILE_NOT_DIRECTORY")
    target = root.joinpath(*components)
    item = {"path": ".", "directory": True, "mode": stat.S_IMODE(root.stat().st_mode) & 0o777,
            "size_bytes": 0} if name == "." else by_name.get(name)
    result = {"operation": operation, "path": name, "data": "", "entries": [], "sha256": ""}
    if operation in {"read", "stat", "list", "chmod"} and item is None:
        raise TransferError("WORKSPACE_FILE_NOT_FOUND")
    if operation in {"read", "write"} and item is not None and item["directory"]:
        raise TransferError("WORKSPACE_FILE_IS_DIRECTORY")
    if operation == "list" and not item["directory"]:
        raise TransferError("WORKSPACE_FILE_NOT_DIRECTORY")
    if operation == "mkdir" and item is not None and not (parents and item["directory"]):
        raise TransferError("WORKSPACE_FILE_EXISTS")
    if operation == "remove":
        if item is None and not recursive:
            raise TransferError("WORKSPACE_FILE_NOT_FOUND")
        if item is not None and item["directory"] and not recursive:
            raise TransferError("WORKSPACE_FILE_IS_DIRECTORY")
    missing_parents = ["/".join(components[:index]) for index in range(1, len(components))
                       if "/".join(components[:index]) not in by_name]
    if operation in {"write", "mkdir"} and missing_parents and not (operation == "mkdir" and parents):
        raise TransferError("WORKSPACE_FILE_NOT_FOUND")
    added = int(item is None) + (len(missing_parents) if operation == "mkdir" else 0)
    if operation in {"write", "mkdir"}:
        size = sum(entry["size_bytes"] for entry in listing) - (item["size_bytes"] if item else 0)
        files = sum(not entry["directory"] for entry in listing)
        if (len(listing) + added > 1024 or operation == "write" and
                (size + len(data) > limit or sum(entry["size_bytes"] for entry in listing) + len(data) > limit
                 or files + int(item is None) > 256)):
            raise TransferError("WORKSPACE_FILE_LIMIT_EXCEEDED")

    # Validation above is side-effect free. Failures below are conservatively
    # unconfirmed, except a bounded read, whose errors never imply a write.
    if operation == "read":
        if item["size_bytes"] > 16 << 20:
            raise TransferError("WORKSPACE_FILE_LIMIT_EXCEEDED")
        try:
            with target.open("rb") as stream:
                body = stream.read((16 << 20) + 1)
        except PermissionError:
            raise TransferError("WORKSPACE_FILE_ACCESS_DENIED") from None
        if len(body) != item["size_bytes"]:
            raise TransferError("WORKSPACE_FILE_OPERATION_UNCONFIRMED")
        result["data"] = base64.b64encode(body).decode("ascii")
        result["sha256"] = hashlib.sha256(body).hexdigest()
        result["entries"] = [{**item, "sha256": result["sha256"]}]
    elif operation == "stat":
        result["entries"] = [item]
    elif operation == "list":
        prefix = "" if name == "." else name + "/"
        result["entries"] = [entry for entry in listing
                             if entry["path"].startswith(prefix) and "/" not in entry["path"][len(prefix):]]
    elif operation == "write":
        temporary = target.parent / (".workspace-write-" + uuid.uuid4().hex)
        try:
            with temporary.open("x+b") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
                stream.seek(0)
                if hashlib.sha256(stream.read()).digest() != hashlib.sha256(data).digest():
                    raise TransferError("WORKSPACE_FILE_OPERATION_UNCONFIRMED")
                written = os.fstat(stream.fileno())
            temporary.chmod(item["mode"] if item else 0o600)
            temporary.replace(target)
        finally:
            if temporary.exists():
                temporary.unlink()
        result["sha256"] = hashlib.sha256(data).hexdigest()
        actual = target.stat()
        if (actual.st_dev, actual.st_ino, actual.st_size) != (written.st_dev, written.st_ino, len(data)):
            raise TransferError("WORKSPACE_FILE_OPERATION_UNCONFIRMED")
        result["entries"] = [{"path": name, "directory": False, "mode": stat.S_IMODE(target.stat().st_mode) & 0o777,
                              "size_bytes": len(data), "sha256": result["sha256"]}]
    elif operation == "mkdir":
        if item is None:
            for parent in missing_parents:
                root.joinpath(*parent.split("/")).mkdir(mode=0o700)
            target.mkdir(mode=0o700)
        result["entries"] = [{"path": name, "directory": True, "mode": stat.S_IMODE(target.stat().st_mode) & 0o777, "size_bytes": 0}]
    elif operation == "remove":
        if item is not None:
            for child in sorted([entry for entry in listing if entry["path"].startswith(name + "/")],
                                key=lambda entry: entry["path"].count("/"), reverse=True):
                child_path = root.joinpath(*child["path"].split("/"))
                child_path.rmdir() if child["directory"] else child_path.unlink()
            target.rmdir() if item["directory"] else target.unlink()
    elif operation == "chmod":
        target.chmod(mode)
        actual_mode = stat.S_IMODE(target.stat().st_mode) & 0o777
        if actual_mode != mode:
            raise TransferError("WORKSPACE_FILE_OPERATION_UNCONFIRMED")
        result["entries"] = [{**item, "mode": actual_mode}]
    return result


def main():
    if (sys.platform != "linux" or os.geteuid() != 65532 or os.getpid() == 1
            or not hasattr(os, "pidfd_open") or not hasattr(signal, "pidfd_send_signal")):
        raise TransferError("WORKSPACE_ADAPTER_UNAVAILABLE")
    operation, raw_limit = sys.argv[1:]
    limit = int(raw_limit)
    if operation not in {"export", "export-output", "hydrate", "file"} or not 1 << 20 <= limit <= 256 << 20:
        raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
    expected, data = None, None
    request = None
    if operation == "file":
        raw = sys.stdin.buffer.read((24 << 20) + 1)
        if len(raw) > 24 << 20:
            raise TransferError("WORKSPACE_FILE_REQUEST_INVALID")
        request = json.loads(raw)
    if operation == "hydrate":
        header = sys.stdin.buffer.readline((1 << 20) + 1)
        if len(header) > 1 << 20 or not header.endswith(b"\n"):
            raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
        plan = json.loads(header)
        expected = plan["entries"]
        size = plan["archive_bytes"]
        if not isinstance(size, int) or not 1024 <= size <= limit + (2 << 20) or len(expected) > 1024:
            raise TransferError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
        data = sys.stdin.buffer.read(size + 1)
        if len(data) != size:
            raise TransferError("WORKSPACE_ARCHIVE_INVALID")
    with quiescent():
        root = Path("/output" if operation == "export-output" else "/workspace")
        if operation == "file":
            result = file_operation(root, request, limit)
        else:
            listing = hydrate_archive(root, data, expected, limit) if operation == "hydrate" else entries(root, limit)
            export_archive(root, listing, sys.stdout.buffer)
    if operation == "file":
        # Do not emit a success receipt until every process we stopped resumed.
        sys.stdout.write(json.dumps(result, ensure_ascii=True, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except BaseException as error:
        code = error.code if isinstance(error, TransferError) else "WORKSPACE_TRANSFER_UNCONFIRMED"
        sys.stderr.write(json.dumps({"code": code}) + "\n")
        sys.exit(1)
