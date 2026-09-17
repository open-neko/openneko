"""Provider-free contract check, run with the pinned Hermes Python environment."""
import ast
import inspect
import textwrap
from datetime import datetime, timezone
from decimal import Decimal
from types import SimpleNamespace
from unittest.mock import patch

from acp.schema import PromptResponse
from acp_adapter.server import HermesACPAgent
from agent import usage_pricing
from agent.codex_runtime import __file__ as codex_runtime_file
from agent.conversation_loop import __file__ as conversation_loop_file
from agent.usage_pricing import CanonicalUsage, estimate_usage_cost

usage = CanonicalUsage(input_tokens=1_000_000, output_tokens=1_000_000, cache_read_tokens=1_000_000)
rates = {
    datetime(2026, 12, 31, 23, 59, 59, tzinfo=timezone.utc): (Decimal("4.575"), "google-pricing-2026-09-17"),
    datetime(2027, 1, 1, tzinfo=timezone.utc): (Decimal("9.15"), "google-pricing-2027-01-01"),
}
for model in ("gemini-3.7-flash", "gemini-3.8-flash"):
    for provider in ("google-gemini", "gemini"):
        for now, (amount, version) in rates.items():
            with patch.object(usage_pricing, "_UTC_NOW", lambda now=now: now):
                result = estimate_usage_cost(model, usage, provider=provider)
            assert result.amount_usd == amount, (model, provider, now, result)
            assert result.status == "estimated" and result.source == "official_docs_snapshot"
            assert result.pricing_version == version
assert estimate_usage_cost("gemini-9-unpriced", usage, provider="google-gemini").amount_usd is None

for path in (codex_runtime_file, conversation_loop_file):
    source = open(path, encoding="utf-8").read()
    assert "session_cost_unknown_calls" in source and "session_pricing_version" in source, path

# Exercise the actual server cost snapshot, including prior-turn counters.
prompt_source = textwrap.dedent(inspect.getsource(HermesACPAgent.prompt))
functions = [
    node for node in ast.walk(ast.parse(prompt_source))
    if isinstance(node, ast.FunctionDef) and node.name in {"agent_number", "cost_usage"}
]
assert len(functions) == 2
agent = SimpleNamespace(
    session_api_calls=4, session_estimated_cost_usd=1.25, session_cost_unknown_calls=1,
    session_cost_status="estimated", session_cost_source="official_docs_snapshot",
    session_pricing_version="google-pricing-2026-09-17", provider="gemini", model="gemini-3.7-flash",
)
namespace = {
    "Any": object, "state": SimpleNamespace(agent=agent),
    "api_calls_start": 4, "cost_start": 1.25, "unknown_cost_calls_start": 1,
}
exec(compile(ast.Module(body=functions, type_ignores=[]), "server cost_usage", "exec"), namespace)
cost_usage = namespace["cost_usage"]
agent.session_api_calls += 2
agent.session_estimated_cost_usd += 0.5
assert cost_usage() == {
    "api_calls": 2, "cost_usd": 0.5, "cost_status": "estimated",
    "cost_source": "official_docs_snapshot", "pricing_version": "google-pricing-2026-09-17",
    "unknown_cost_calls": 0, "provider": "gemini", "model": "gemini-3.7-flash",
}
agent.session_cost_unknown_calls += 1
assert cost_usage()["cost_status"] == "unknown" and cost_usage()["unknown_cost_calls"] == 1
agent.session_estimated_cost_usd = object()
assert cost_usage()["cost_usd"] == 0.0  # a non-numeric counter never raises

assert "field_meta=field_meta" in prompt_source and '{"openneko": {"usage": cost_usage()}}' in prompt_source
payload = PromptResponse(stop_reason="end_turn", field_meta={"openneko": {"usage": cost_usage()}})
assert payload.model_dump(by_alias=True, exclude_none=True)["_meta"]["openneko"]["usage"]["cost_status"] == "unknown"
print("Hermes cost contract passed")
