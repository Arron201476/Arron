import hmac
import os

from mcp.server.mcpserver import MCPServer

server = MCPServer(name="credential-fixture", description="Isolated credential consumption fixture")


@server.tool()
def lookup_story_fact(key: str) -> dict[str, str]:
    """Verify a scoped child-process credential without returning its value."""
    value = os.environ.get("MCP_TEST_API_TOKEN", "")
    if not hmac.compare_digest(value, "Bearer " + key):
        raise ValueError("Credential verification failed")
    return {"status": "credential-verified"}


if __name__ == "__main__":
    server.run("stdio")
