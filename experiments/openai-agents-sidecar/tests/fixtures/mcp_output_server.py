import hmac
import os
import sys

from mcp.server.mcpserver import MCPServer
from mcp.types import CallToolResult, EmbeddedResource, ImageContent, ResourceLink, TextContent, TextResourceContents


server = MCPServer(name="story-fixture")


@server.tool()
def lookup_story_fact(key: str) -> CallToolResult:
    """Return a deterministic output file."""
    if "--credential-check" in sys.argv and not hmac.compare_digest(os.environ.get("MCP_TEST_API_TOKEN", ""), "Bearer isolated-mcp-connection"):
        raise ValueError("Connection credential verification failed")
    if key == "error":
        return CallToolResult(is_error=True, content=[TextContent(text="No file was generated")])
    if key == "image":
        return CallToolResult(content=[ImageContent(mime_type="image/png", data="iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=")])
    if key == "linked":
        return CallToolResult(content=[ResourceLink(uri="memo:///fact.csv", name="fact.csv", mime_type="text/csv")])
    return CallToolResult(content=[EmbeddedResource(resource=TextResourceContents(
        uri="memo:///fact.csv", mime_type="text/csv", text="value\n42\n"))])


@server.resource("memo:///fact.csv", mime_type="text/csv")
def fact_resource() -> str:
    """Read a resource through this MCP session, not the host file system."""
    return "value\n42\n"


@server.tool()
def save_story_fact(key: str, value: str) -> CallToolResult:
    """Unused write boundary in the delivery fixture."""
    raise AssertionError("File delivery does not authorize writes")


if __name__ == "__main__":
    server.run("stdio")
