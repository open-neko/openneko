"""Provider-free contract check, run with the pinned Hermes Python environment."""
import ast
import inspect
import textwrap
from types import SimpleNamespace
from unittest.mock import patch

from acp_adapter.events import make_step_cb, make_tool_progress_cb
from acp_adapter.server import HermesACPAgent

# Exercise the actual server snapshot expression, including prior-turn counters.
tree = ast.parse(textwrap.dedent(inspect.getsource(HermesACPAgent.prompt)))
getter = next(node.value for node in ast.walk(tree) if isinstance(node, ast.keyword) and node.arg == "usage_getter")
agent = SimpleNamespace(session_api_calls=2, session_input_tokens=500, session_output_tokens=50)
fields = ("input", "output")
usage_getter = eval(compile(ast.Expression(getter), "server usage_getter", "eval"), {
    "state": SimpleNamespace(agent=agent), "usage_calls_start": 2,
    "usage_fields": fields, "usage_start": {"input": 500, "output": 50},
    "cost_usage": lambda: {},
})
assert usage_getter() is None  # no measured response is not zero-token usage
agent.session_api_calls += 1
agent.session_input_tokens += 120
agent.session_output_tokens += 30
assert usage_getter() == {"input_tokens": 120, "output_tokens": 30}

with patch("acp_adapter.events._send_update") as send:
    callback = make_tool_progress_cb(None, "test", None, {}, {}, usage_getter=usage_getter)
    callback("tool.started", "terminal", args={"command": "true"})
    payload = send.call_args.args[-1].model_dump(by_alias=True, exclude_none=True)
    assert payload["_meta"]["openneko"]["usage"] == {"input_tokens": 120, "output_tokens": 30}
    callback = make_tool_progress_cb(None, "test", None, {}, {}, usage_getter=lambda: None)
    callback("tool.started", "terminal", args={})
    assert "_meta" not in send.call_args.args[-1].model_dump(by_alias=True, exclude_none=True)
    ids, meta = {}, {}
    callback = make_tool_progress_cb(None, "test", None, ids, meta)
    callback("tool.started", "mcp__neko__data_source_manager_list_data_sources", args={})
    started = send.call_args.args[-1].model_dump(by_alias=True, exclude_none=True)
    callback("tool.completed", "mcp__neko__data_source_manager_list_data_sources",
             result='{"sources":[{"name":"default"}]}')
    completed = send.call_args.args[-1].model_dump(by_alias=True, exclude_none=True)
    assert completed["toolCallId"] == started["toolCallId"]
    assert completed["status"] == "completed"
    assert "default" in str(completed.get("content") or completed.get("rawOutput"))
    count = send.call_count
    make_step_cb(None, "test", None, ids, meta)(1, [{"name": "mcp__neko__data_source_manager_list_data_sources", "result": "duplicate"}])
    assert send.call_count == count
    callback("tool.started", "mcp__neko__data_source_manager_list_data_sources", args={})
    callback("tool.completed", "mcp__neko__data_source_manager_list_data_sources",
             result="service unavailable", is_error=True)
    failed = send.call_args.args[-1].model_dump(by_alias=True, exclude_none=True)
    assert failed["status"] == "failed"
print("Hermes tool usage contract passed")
