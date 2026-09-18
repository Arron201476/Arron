from copy import deepcopy

import pytest

from content_agent_sidecar.native_pause import unstarted_checkpoint, validate_unstarted_input


def empty_state():
    return {
        "current_turn": 0, "current_step": None, "last_processed_response": None,
        "last_model_response": None, "model_responses": [], "generated_items": [],
        "session_items": [], "pending_input": [],
        "original_input": [{"role": "user", "content": "history"}, {"role": "user", "content": "initial"}],
    }


@pytest.mark.parametrize("field,value", [
    ("current_turn", True), ("current_turn", 1), ("current_step", {"type": "run_again"}),
    ("last_processed_response", {}), ("last_model_response", {}),
    ("model_responses", [{}]), ("generated_items", [{}]),
    ("session_items", [{}]), ("pending_input", [{"role": "user", "content": "pending"}]),
])
def test_only_zero_work_checkpoint_can_reenter_initial_runner(field, value):
    state = empty_state()
    state[field] = value
    assert not unstarted_checkpoint(state)
    with pytest.raises(ValueError, match="Unstarted checkpoint"):
        validate_unstarted_input(state, "initial")


def test_zero_checkpoint_validation_is_read_only_and_preserves_exact_input():
    state = empty_state()
    before = deepcopy(state)
    validate_unstarted_input(state, "initial")
    validate_unstarted_input(state, [{"role": "user", "content": "initial"}])
    for invalid in (None, [], "different", [{"role": "assistant", "content": "initial"}]):
        with pytest.raises(ValueError):
            validate_unstarted_input(state, invalid)
    assert state == before
