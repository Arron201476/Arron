from __future__ import annotations

import asyncio
import base64
import binascii
import struct
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, Protocol
from weakref import WeakSet

from agents import ComputerTool
from agents.computer import AsyncComputer, Button
from agents.tool import ComputerProvider, ComputerToolSafetyCheckData


class ComputerSessionError(RuntimeError):
    pass


@dataclass(frozen=True)
class ComputerSessionBinding:
    session_id: str
    generation: int
    width: int
    height: int

    def __post_init__(self):
        if (not isinstance(self.session_id, str) or not self.session_id or len(self.session_id) > 256
                or type(self.generation) is not int or self.generation < 1
                or any(type(value) is not int or not 1 <= value <= 4096 for value in (self.width, self.height))):
            raise ComputerSessionError("Invalid managed computer session binding")


class ComputerSessionTransport(Protocol):
    """Runtime must authorize and audit each action before executing it.

    Implementations own an isolated browser, not the sidecar host desktop. An
    action receipt confirms its exact session generation and sequence. Neither
    action execution nor an unknown outcome may be automatically replayed.
    """

    async def execute(self, binding: ComputerSessionBinding, sequence: int, action: dict[str, Any]) -> dict[str, Any]: ...

    async def close(self, binding: ComputerSessionBinding) -> None: ...


def managed_computer_tool(
    open_session: Callable[[Any], Awaitable[ManagedComputer]],
    authorize_safety_check: Callable[[ComputerToolSafetyCheckData], Awaitable[bool]],
) -> ComputerTool:
    opened: WeakSet[ManagedComputer] = WeakSet()

    async def create(*, run_context):
        context = run_context.context
        modes = [getattr(context, key, "") for key in ("agent_turn_id", "agent_task_attempt_id", "execution_attempt_id")]
        if (not getattr(context, "project_id", "") or not getattr(context, "conversation_id", "")
                or sum(bool(value) for value in modes) != 1 or getattr(context, "memory_generation_id", "")
                or (any(modes[1:]) and not getattr(context, "attempt_token", ""))):
            raise ComputerSessionError("Computer requires one authorized execution identity")
        computer = await open_session(context)
        if not isinstance(computer, ManagedComputer) or computer in opened or computer._closed:
            raise ComputerSessionError("Computer opener did not return a fresh managed session")
        opened.add(computer)
        return computer

    async def dispose(*, run_context, computer):
        await computer.close()

    async def check(data):
        # A nonempty object/string from a remote API is not an approval.
        return await authorize_safety_check(data) is True

    return ComputerTool(computer=ComputerProvider(create=create, dispose=dispose), on_safety_check=check)


class ManagedComputer(AsyncComputer):
    def __init__(self, binding: ComputerSessionBinding, transport: ComputerSessionTransport, *, timeout_seconds: float = 30):
        if not 0 < timeout_seconds <= 120:
            raise ComputerSessionError("Invalid managed computer timeout")
        self.binding = binding
        self._transport = transport
        self._timeout = timeout_seconds
        self._sequence = 0
        self._closed = False
        self._cleanup_complete = False
        self._active: asyncio.Task | None = None
        self._lock = asyncio.Lock()

    @property
    def environment(self):
        return "browser"

    @property
    def dimensions(self):
        return self.binding.width, self.binding.height

    def _point(self, x: int, y: int) -> None:
        if type(x) is not int or type(y) is not int or not 0 <= x < self.binding.width or not 0 <= y < self.binding.height:
            raise ComputerSessionError("Computer coordinates are outside the managed viewport")

    @staticmethod
    def _keys(keys: list[str] | None) -> list[str]:
        if keys is None:
            return []
        if (not isinstance(keys, list) or len(keys) > 16
                or any(not isinstance(key, str) or not key or len(key) > 64 or not key.isprintable() for key in keys)):
            raise ComputerSessionError("Invalid computer key combination")
        return list(keys)

    async def _execute(self, action: dict[str, Any]) -> dict[str, Any]:
        async with self._lock:
            if self._closed:
                raise ComputerSessionError("Managed computer session is closed")
            self._sequence += 1
            try:
                self._active = asyncio.create_task(self._transport.execute(self.binding, self._sequence, action))
                receipt = await asyncio.wait_for(self._active, self._timeout)
                if self._closed:
                    raise ComputerSessionError("Managed computer was stopped during action")
                if (not isinstance(receipt, dict) or receipt.get("session_id") != self.binding.session_id
                        or type(receipt.get("generation")) is not int or receipt["generation"] != self.binding.generation
                        or type(receipt.get("sequence")) is not int or receipt["sequence"] != self._sequence
                        or receipt.get("status") != "completed" or receipt.get("authorized") is not True):
                    raise ComputerSessionError("Managed computer action receipt is unconfirmed")
                if action["type"] == "screenshot":
                    self._validate_screenshot(receipt.get("png_base64"))
                return receipt
            except BaseException as exc:
                self._closed = True
                try:
                    await self._close_transport()
                except Exception as cleanup_error:
                    exc.add_note(f"Managed computer cleanup unconfirmed ({type(cleanup_error).__name__})")
                raise
            finally:
                self._active = None

    async def _close_transport(self) -> None:
        await asyncio.wait_for(self._transport.close(self.binding), self._timeout)
        self._cleanup_complete = True

    async def close(self) -> None:
        self._closed = True
        if self._active is not None and not self._active.done() and not self._active.cancelling():
            self._active.cancel()
        async with self._lock:
            if self._cleanup_complete:
                return
            self._closed = True
            await self._close_transport()

    async def screenshot(self) -> str:
        receipt = await self._execute({"type": "screenshot"})
        return receipt["png_base64"]

    def _validate_screenshot(self, encoded: Any) -> None:
        try:
            if not isinstance(encoded, str) or len(encoded) > 24 * 1024 * 1024:
                raise ValueError("Invalid screenshot size")
            payload = base64.b64decode(encoded, validate=True)
            if (len(payload) < 33 or payload[:16] != b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
                    or struct.unpack(">II", payload[16:24]) != self.dimensions):
                raise ValueError("Screenshot does not match the managed viewport")
        except (ValueError, binascii.Error) as exc:
            raise ComputerSessionError("Invalid managed computer screenshot") from exc

    async def click(self, x: int, y: int, button: Button = "left", *, keys: list[str] | None = None) -> None:
        self._point(x, y)
        if button not in {"left", "right", "wheel", "back", "forward"}:
            raise ComputerSessionError("Invalid computer mouse button")
        await self._execute({"type": "click", "x": x, "y": y, "button": button, "keys": self._keys(keys)})

    async def double_click(self, x: int, y: int, *, keys: list[str] | None = None) -> None:
        self._point(x, y)
        await self._execute({"type": "double_click", "x": x, "y": y, "keys": self._keys(keys)})

    async def scroll(self, x: int, y: int, scroll_x: int, scroll_y: int, *, keys: list[str] | None = None) -> None:
        self._point(x, y)
        if any(type(value) is not int or abs(value) > 100000 for value in (scroll_x, scroll_y)):
            raise ComputerSessionError("Invalid computer scroll distance")
        await self._execute({"type": "scroll", "x": x, "y": y, "scroll_x": scroll_x, "scroll_y": scroll_y, "keys": self._keys(keys)})

    async def type(self, text: str) -> None:
        if not isinstance(text, str) or len(text.encode("utf-8")) > 65536:
            raise ComputerSessionError("Invalid computer text input")
        await self._execute({"type": "type", "text": text})

    async def wait(self) -> None:
        await self._execute({"type": "wait"})

    async def move(self, x: int, y: int, *, keys: list[str] | None = None) -> None:
        self._point(x, y)
        await self._execute({"type": "move", "x": x, "y": y, "keys": self._keys(keys)})

    async def keypress(self, keys: list[str]) -> None:
        await self._execute({"type": "keypress", "keys": self._keys(keys)})

    async def drag(self, path: list[tuple[int, int]], *, keys: list[str] | None = None) -> None:
        if not isinstance(path, list) or not 2 <= len(path) <= 512:
            raise ComputerSessionError("Invalid computer drag path")
        points = []
        for point in path:
            if not isinstance(point, (tuple, list)) or len(point) != 2:
                raise ComputerSessionError("Invalid computer drag point")
            self._point(*point)
            points.append(list(point))
        await self._execute({"type": "drag", "path": points, "keys": self._keys(keys)})
