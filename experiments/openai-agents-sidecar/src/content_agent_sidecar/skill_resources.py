from __future__ import annotations

import base64
import json
from typing import Any

from agents.tool import ToolOutputFileContent, ToolOutputImage, ToolOutputText


MAX_RESOURCE_BYTES = 10 * 1024 * 1024
IMAGE_MEDIA = {"image/png", "image/jpeg", "image/gif", "image/webp"}
FILE_MEDIA = {"application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}


def skill_resource_output(payload: dict[str, Any]) -> Any:
    data = payload.get("data")
    if not isinstance(data, dict) or not data.get("data_base64"):
        return json.dumps(payload, ensure_ascii=False)
    encoded = data["data_base64"]
    if not isinstance(encoded, str) or len(encoded) > ((MAX_RESOURCE_BYTES + 2) // 3) * 4:
        raise ValueError("Skill resource exceeds 10 MiB")
    content = base64.b64decode(encoded, validate=True)
    if not content or len(content) > MAX_RESOURCE_BYTES:
        raise ValueError("Invalid bounded Skill resource")
    media_type, kind = data.get("media_type"), data.get("kind")
    uri = f"data:{media_type};base64,{encoded}"
    metadata = {key: value for key, value in data.items() if key != "data_base64"}
    metadata["size_bytes"] = len(content)
    text = ToolOutputText(text=json.dumps(metadata, ensure_ascii=False))
    if kind == "image" and media_type in IMAGE_MEDIA:
        return [text, ToolOutputImage(image_url=uri, detail="auto")]
    if kind == "file" and media_type in FILE_MEDIA:
        filename = str(data.get("filename") or "")
        if not filename or any(char in filename for char in "/\\:\x00") or filename in {".", ".."}:
            raise ValueError("Invalid Skill resource filename")
        return [text, ToolOutputFileContent(file_data=uri, filename=filename)]
    raise ValueError("Unsupported binary Skill resource")
