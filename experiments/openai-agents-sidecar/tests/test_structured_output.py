from content_agent_sidecar.structured_output import parse_control_decision


def test_parse_control_decision() -> None:
    decision = parse_control_decision(
        '{"reply":"使用小说转剧本","intent":"propose_capability",'
        '"confidence":0.91,"capability_id":"novel_to_script"}'
    )
    assert decision.intent == "propose_capability"
    assert decision.capability_id == "novel_to_script"
