"""OpenAI Agents SDK sidecar spike."""
from __future__ import annotations

import os


# The SDK defaults to including model/tool payloads in traces. This service may
# process unpublished scripts, so redaction is the process default even when
# tracing is explicitly enabled.
os.environ.setdefault("OPENAI_AGENTS_TRACE_INCLUDE_SENSITIVE_DATA", "0")
os.environ.setdefault("OPENAI_AGENTS_DONT_LOG_MODEL_DATA", "1")
os.environ.setdefault("OPENAI_AGENTS_DONT_LOG_TOOL_DATA", "1")
