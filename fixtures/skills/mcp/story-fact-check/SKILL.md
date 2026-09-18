---
name: story-fact-check
description: Check story facts through an operator-managed MCP service and save corrections only after approval.
---

# Story Fact Check

Use the configured story fact service to verify names, relationships, and continuity.
Read facts before making claims. When the user requests a correction, invoke the
write tool to request SDK approval. The SDK pauses before performing the write;
do not replace the approval request with a chat message. Resume the same tool
call only after approval, and report rejection without claiming a successful save.
