from content_agent_sidecar.events import agent_event


def test_agent_event_envelope_is_scoped_and_provider_independent() -> None:
    event = agent_event(
        "agent.tool.started",
        project_id="prj_1",
        conversation_id="conv_1",
        turn_id="turn_1",
        payload={"name": "inspect_project"},
    ).model_dump(mode="json")

    assert event["schema_version"] == "1.0.0"
    assert event["event_type"] == "agent.tool.started"
    assert event["project_id"] == "prj_1"
    assert event["payload"] == {"name": "inspect_project"}
    assert "provider" not in event
    assert event["terminal"] is False


def test_input_receipt_is_a_nonterminal_sidecar_event():
    event = agent_event("agent.turn.inputs_included", project_id="project", conversation_id="conversation",
                        turn_id="turn", payload={"included_input_ids": ["input-1"]})
    assert event.event_type == "agent.turn.inputs_included"
    assert not event.terminal and event.payload == {"included_input_ids": ["input-1"]}
