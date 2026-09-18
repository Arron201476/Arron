package runtime

// Resolve only public ownership from durable bindings, never SDK state or lease credentials.
const agentToolExecutionSelect = `COALESCE(
	(SELECT json_object('mode', 'stateful_workflow',
		'attempt_id', ea.attempt_id, 'attempt_no', ea.attempt_no,
		'run_id', r.run_id, 'step_run_id', sr.step_run_id, 'step_id', sr.step_id,
		'task_item_id', ti.task_item_id, 'item_key', ti.item_key, 'capability_id', r.capability_id)
	 FROM execution_tool_calls ec
	 JOIN execution_attempts ea ON ea.attempt_id = ec.execution_attempt_id
	 JOIN runs r ON r.run_id = ea.run_id
	 JOIN projects p ON p.project_id = r.project_id
	 JOIN step_runs sr ON sr.step_run_id = ea.step_run_id AND sr.run_id = r.run_id
	 JOIN task_items ti ON ti.task_item_id = ea.task_item_id AND ti.run_id = r.run_id AND ti.step_run_id = sr.step_run_id
	 WHERE ec.agent_tool_call_id = agent_tool_calls.agent_tool_call_id
		AND r.project_id = agent_tool_calls.project_id AND r.conversation_id = agent_tool_calls.conversation_id
		AND p.workspace_id = agent_tool_calls.workspace_id),
	(SELECT json_object('mode', 'background_task', 'agent_task_id', task.agent_task_id,
		'attempt_id', attempt.agent_task_attempt_id, 'attempt_no', attempt.attempt_no, 'capability_id', task.capability_id)
	 FROM agent_task_tool_calls tc
	 JOIN agent_task_attempts attempt ON attempt.agent_task_attempt_id = tc.agent_task_attempt_id
	 JOIN agent_tasks task ON task.agent_task_id = attempt.agent_task_id
	 WHERE tc.agent_tool_call_id = agent_tool_calls.agent_tool_call_id
		AND task.project_id = agent_tool_calls.project_id AND task.conversation_id = agent_tool_calls.conversation_id
		AND task.workspace_id = agent_tool_calls.workspace_id),
	(SELECT json_object('mode', 'conversation', 'agent_turn_id', turn.agent_turn_id)
	 FROM agent_turns turn
	 WHERE turn.agent_turn_id = agent_tool_calls.agent_turn_id
		AND turn.project_id = agent_tool_calls.project_id AND turn.conversation_id = agent_tool_calls.conversation_id
		AND turn.workspace_id = agent_tool_calls.workspace_id)
)`
