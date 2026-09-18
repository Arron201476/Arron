from __future__ import annotations

from pathlib import Path, PurePosixPath
from typing import Literal

from agents.sandbox.entries import BaseEntry, Dir, File
from agents.sandbox.manifest import Manifest
from agents.sandbox.types import Permissions
from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from .backend import BackendError
from .native_files import MAX_FILE_BYTES, _go_json_hash, portable_file_path

MEMORY_DIRECTORY = ".agent-memory"
GENERATION_DIRECTORY = ".agent-memory-input"


class _ResourceModel(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid", strict=True, hide_input_in_errors=True)


class NativeSkillSource(_ResourceModel):
    capability_id: str = Field(min_length=1, max_length=256)
    version: str = Field(min_length=1, max_length=256)
    content_hash: str = Field(pattern=r"^sha256:[a-f0-9]{64}$")


class NativeProjectSource(_ResourceModel):
    path: str
    version: int = Field(ge=1)
    content_hash: str = Field(pattern=r"^(sha256:)?[a-f0-9]{64}$")

    @field_validator("path")
    @classmethod
    def check_path(cls, value):
        if not portable_file_path(value):
            raise ValueError("Project source must have a portable workspace-relative path")
        if value.split("/")[0].lower() in {".skills", MEMORY_DIRECTORY}:
            raise ValueError("Project files cannot replace the Skill or private memory namespace")
        return value


class NativeMemorySource(_ResourceModel):
    version: int = Field(ge=1)
    content_hash: str = Field(pattern=r"^[a-f0-9]{64}$")


class NativeGenerationSource(_ResourceModel):
    generation_id: str = Field(pattern=r"^[A-Za-z0-9_-]{1,256}$")
    source_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    extraction_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    plan_hash: str | None = Field(default=None, pattern=r"^[a-f0-9]{64}$")


class NativeManifestSources(_ResourceModel):
    skills: list[NativeSkillSource] = Field(default_factory=list, max_length=32)
    files: list[NativeProjectSource] = Field(default_factory=list, max_length=256)
    memory: NativeMemorySource | None = None
    generation: NativeGenerationSource | None = None


class NativeManifestFile(_ResourceModel):
    path: str
    sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    size_bytes: int = Field(ge=0, le=MAX_FILE_BYTES)
    skill: NativeSkillSource | None = None
    resource_path: str | None = None
    project_file: NativeProjectSource | None = None
    memory: NativeMemorySource | None = None
    generation: NativeGenerationSource | None = None

    @model_validator(mode="after")
    def validate_source(self):
        if not portable_file_path(self.path):
            raise ValueError("Manifest destination must have a portable workspace-relative path")
        if sum(source is not None for source in (self.skill, self.project_file, self.memory, self.generation)) != 1:
            raise ValueError("Each resource requires exactly one versioned source")
        if self.generation:
            if self.generation.plan_hash:
                allowed = (self.path.startswith(GENERATION_DIRECTORY + "/") and self.path.endswith(".jsonl")
                    or self.path.startswith(MEMORY_DIRECTORY + "/raw_memories/") and self.path.endswith(".md")
                    or self.path.startswith(MEMORY_DIRECTORY + "/rollout_summaries/") and self.path.endswith(".md")
                    or self.path in {MEMORY_DIRECTORY + "/MEMORY.md", MEMORY_DIRECTORY + "/memory_summary.md", MEMORY_DIRECTORY + "/raw_memories.md"})
                if not allowed or self.resource_path != self.path:
                    raise ValueError("Planned memory input differs from its private destination")
            elif self.resource_path not in {"rollout.jsonl", "extraction.json"} or self.path != f"{GENERATION_DIRECTORY}/{self.resource_path}":
                raise ValueError("Generation input differs from its private destination")
        elif self.memory:
            if not portable_file_path(self.resource_path) or self.path != f"{MEMORY_DIRECTORY}/{self.resource_path}":
                raise ValueError("Memory resource differs from its private destination")
        elif self.skill:
            if not portable_file_path(self.resource_path):
                raise ValueError("Skill resource must have a portable workspace-relative path")
            if (not self.path.startswith(".skills/") or len(PurePosixPath(self.path).parts) < 3
                    or "/".join(PurePosixPath(self.path).parts[2:]) != self.resource_path):
                raise ValueError("Skill resource path differs from its destination")
        elif self.resource_path is not None or self.path != self.project_file.path:
            raise ValueError("Project source differs from its destination")
        return self


class RuntimeResourceFile(BaseEntry):
    """SDK entry with a durable source reference; binary bytes stay out of state.

    The actual File.apply and metadata operations remain SDK-owned. The Runtime
    checks every initial write against its one-time, versioned inventory.
    """

    type: Literal["content_agent_resource"] = "content_agent_resource"
    resource: NativeManifestFile
    manifest_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    permissions: Permissions = Field(default_factory=lambda: Permissions.from_mode(0o644))

    async def apply(self, session, dest: Path, base_dir: Path):
        if (getattr(session, "_materializing", "") != self.manifest_hash
                or session._relative_path(dest, for_write=True) != self.resource.path):
            raise BackendError("Resource materialization is not active for this SDK destination")
        body = await session.transport.read_manifest_file(self.manifest_hash, self.resource)
        return await File(content=body, permissions=self.permissions).apply(session, dest, base_dir)


class NativeManifestInventory(_ResourceModel):
    session_id: str = Field(pattern=r"^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$")
    manifest_hash: str = Field(pattern=r"^[a-f0-9]{64}$")
    files: list[NativeManifestFile] = Field(max_length=256)

    @model_validator(mode="after")
    def validate_inventory(self):
        files = [file.model_dump(exclude_none=True) for file in self.files]
        if _go_json_hash(files) != self.manifest_hash or sum(file.size_bytes for file in self.files) > MAX_FILE_BYTES:
            raise ValueError("Resource inventory hash or size is invalid")
        kinds: dict[str, tuple[str, bool]] = {}
        for file in self.files:
            name = PurePosixPath(file.path)
            for index, entry in enumerate((name, *name.parents)):
                if str(entry) == ".":
                    continue
                key, directory = str(entry).lower(), index > 0
                previous = kinds.get(key)
                if previous and (previous != (str(entry), directory) or not directory):
                    raise ValueError("Initial resources have overlapping or aliased paths")
                kinds[key] = (str(entry), directory)
        if len(kinds) > 1024:
            raise ValueError("Resource inventory exceeds its directory limit")
        return self

    def manifest(self) -> Manifest:
        root: dict[str, BaseEntry] = {}
        for file in self.files:
            children = root
            *parents, name = PurePosixPath(file.path).parts
            for parent in parents:
                directory = children.setdefault(parent, Dir())
                children = directory.children
            children[name] = RuntimeResourceFile(resource=file, manifest_hash=self.manifest_hash)
        return Manifest(root="/workspace", entries=root)


def resource_manifest_hash(manifest: Manifest) -> str:
    hashes = {entry.manifest_hash for _, entry in manifest.iter_entries() if type(entry) is RuntimeResourceFile}
    if len(hashes) > 1:
        raise BackendError("One SDK manifest cannot combine different initialization grants")
    return next(iter(hashes), "")
