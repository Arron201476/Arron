import hashlib
import json


class EmptyInstructionsBackend:
    async def resolve_agent_instruction_snapshot(self, project_id, activity_key):
        result = {"activity_key": activity_key, "project_id": project_id, "workspace_id": "test-workspace",
                  "user_id": "test-user", "documents": []}
        result["content_hash"] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
        return result
