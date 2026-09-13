"""Provider-free contract check, run with the pinned Hermes Python environment."""
import ast
import inspect
import textwrap
from types import SimpleNamespace
from unittest.mock import patch

from acp_adapter.events import make_tool_progress_cb
from acp_adapter.server import HermesACPAgent

# Exercise the actual server snapshot expression, including prior-turn counters.
tree = ast.parse(textwrap.dedent(inspect.getsource(HermesACPAgent.prompt)))
getter = next(node.value for node in ast.walk(tree) if isinstance(node, ast.keyword) and node.arg == "usage_getter")
agent = SimpleNamespace(session_api_calls=2, session_input_tokens=500, session_output_tokens=50)
fields = ("input", "output")
usage_getter = eval(compile(ast.Expression(getter), "server usage_getter", "eval"), {
    "state": SimpleNamespace(agent=agent), "usage_calls_start": 2,
    "usage_fields": fields, "usage_start": {"input": 500, "output": 50},
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
print("Hermes tool usage contract passed")
