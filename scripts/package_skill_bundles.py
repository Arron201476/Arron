from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import zipfile


SKILLS = (
    "novel-to-script",
    "non-novel-to-script",
    "video-reference-creation",
    "script-continuation",
)


def walk_values(value):
    if isinstance(value, dict):
        for key, item in value.items():
            yield key, item
            yield from walk_values(item)
    elif isinstance(value, list):
        for item in value:
            yield None, item
            yield from walk_values(item)


def path_reference(value: object) -> str | None:
    if not isinstance(value, str) or not value.startswith("."):
        return None
    return value.split("#", 1)[0]


def copy_file(project_root: Path, source: Path, bundle_root: Path) -> Path:
    relative = source.resolve().relative_to(project_root)
    destination = bundle_root / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)
    return destination


def collect_json_dependencies(
    project_root: Path,
    source: Path,
    bundle_root: Path,
    copied: set[Path],
) -> dict:
    source = source.resolve()
    if source in copied:
        return json.loads(source.read_text(encoding="utf-8"))
    copied.add(source)
    copy_file(project_root, source, bundle_root)
    payload = json.loads(source.read_text(encoding="utf-8"))
    for _key, value in walk_values(payload):
        reference = path_reference(value)
        if not reference:
            continue
        dependency = (source.parent / reference).resolve()
        dependency.relative_to(project_root)
        if not dependency.is_file():
            raise FileNotFoundError(f"missing dependency: {value} from {source}")
        if dependency.suffix.lower() == ".json":
            collect_json_dependencies(project_root, dependency, bundle_root, copied)
        elif dependency not in copied:
            copied.add(dependency)
            copy_file(project_root, dependency, bundle_root)
    return payload


def write_json(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def build_bundle(project_root: Path, output_root: Path, skill: str) -> Path:
    manifest_source = project_root / "capabilities" / "v1" / f"{skill}.json"
    bundle_root = output_root / skill
    if bundle_root.exists():
        raise FileExistsError(f"output already exists: {bundle_root}")
    bundle_root.mkdir(parents=True)

    copied: set[Path] = set()
    manifest = collect_json_dependencies(
        project_root, manifest_source, bundle_root, copied
    )
    adapter_ids = {
        value
        for key, value in walk_values(manifest)
        if key == "response_adapter_ref" and isinstance(value, str)
    }
    artifact_types = {
        value
        for key, value in walk_values(manifest)
        if key == "artifact_type" and isinstance(value, str)
    }

    adapter_source = project_root / "capabilities" / "v1" / "response-adapters.json"
    adapter_registry = json.loads(adapter_source.read_text(encoding="utf-8"))
    adapters = [
        item for item in adapter_registry["adapters"] if item["id"] in adapter_ids
    ]
    found_adapter_ids = {item["id"] for item in adapters}
    if found_adapter_ids != adapter_ids:
        missing = sorted(adapter_ids - found_adapter_ids)
        raise ValueError(f"missing response adapters: {missing}")
    filtered_adapters = {
        "contract_version": adapter_registry["contract_version"],
        "definition_status": adapter_registry["definition_status"],
        "adapters": adapters,
    }
    adapter_output = bundle_root / "capabilities" / "v1" / "response-adapters.json"
    write_json(adapter_output, filtered_adapters)
    for adapter in adapters:
        reference = path_reference(adapter.get("target_schema_ref"))
        if reference:
            dependency = (adapter_source.parent / reference).resolve()
            collect_json_dependencies(project_root, dependency, bundle_root, copied)

    presentation_source = (
        project_root
        / "capabilities"
        / "v1"
        / "registries"
        / "artifact-presentations.json"
    )
    presentation_registry = json.loads(
        presentation_source.read_text(encoding="utf-8")
    )
    presentations = [
        item
        for item in presentation_registry["presentations"]
        if item["artifact_type"] in artifact_types
    ]
    filtered_presentations = {
        "contract_version": presentation_registry["contract_version"],
        "definition_status": presentation_registry["definition_status"],
        "presentations": presentations,
    }
    write_json(
        bundle_root
        / "capabilities"
        / "v1"
        / "registries"
        / "artifact-presentations.json",
        filtered_presentations,
    )

    write_json(
        bundle_root / "bundle.json",
        {
            "bundle_format": "content-agent-capability-bundle/v1",
            "capability_id": manifest["id"],
            "capability_version": manifest["version"],
            "entry_manifest": f"capabilities/v1/{skill}.json",
        },
    )
    (bundle_root / "README.md").write_text(
        f"# {manifest['label']}\n\n"
        f"Entry manifest: `capabilities/v1/{skill}.json`\n\n"
        "This archive contains only this capability's referenced prompts, rules, "
        "schemas, response adapters, and artifact presentations. Preserve the "
        "directory layout when importing it. A compatible Content Agent runtime "
        "must implement the executor_ref values declared by the manifest.\n",
        encoding="utf-8",
    )

    zip_path = output_root / f"{skill}.zip"
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as archive:
        for file_path in sorted(bundle_root.rglob("*")):
            if file_path.is_file():
                archive.write(file_path, file_path.relative_to(bundle_root))
    return zip_path


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("output_root", type=Path)
    args = parser.parse_args()
    project_root = Path(__file__).resolve().parents[1]
    output_root = args.output_root.resolve()
    output_root.relative_to(project_root)
    if output_root.exists():
        raise FileExistsError(f"output already exists: {output_root}")
    output_root.mkdir(parents=True)
    for skill in SKILLS:
        archive = build_bundle(project_root, output_root, skill)
        print(archive)


if __name__ == "__main__":
    main()
