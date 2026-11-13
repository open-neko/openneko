"""Query OpenNeko data from a skill script.

    from neko_data import query
    rows = query("query { orders(limit: 10) { id } }")["orders"]

In an OpenNeko run, the script holds no key and no socket. When it asks for
rows that the run has not fetched yet, it stops with its request. OpenNeko
runs the query under the run's identity and policy, and runs the script
again with the rows. Outside OpenNeko, set NEKO_DATA_GRAPHJIN_URL to a
GraphJin GraphQL URL and the script calls it directly.
"""
import hashlib
import json
import os
import sys
import urllib.error
import urllib.request

__all__ = ["NekoDataError", "query"]

WAIT_EXIT_CODE = 75
_DATA_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.realpath(__file__))), ".neko-data")


class NekoDataError(RuntimeError):
    pass


def _request(graphql, variables, namespace):
    return {"query": graphql, "variables": variables or None, "namespace": namespace or None}


def _key(request):
    return hashlib.sha256(
        json.dumps(request, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")
    ).hexdigest()


def _from_run(request):
    key = _key(request)
    answer = os.path.join(_DATA_DIR, "answers", key + ".json")
    try:
        with open(answer, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        pass
    except ValueError:
        raise NekoDataError("the recorded answer for this query is damaged")
    requests = os.path.join(_DATA_DIR, "requests")
    os.makedirs(requests, exist_ok=True)
    body = {k: v for k, v in request.items() if v is not None}
    partial = os.path.join(requests, key + ".part")
    with open(partial, "w", encoding="utf-8") as f:
        json.dump(body, f)
    os.replace(partial, os.path.join(requests, key + ".json"))
    sys.stdout.flush()
    sys.stderr.write("neko_data: OpenNeko runs this query and starts the script again\n")
    sys.stderr.flush()
    os._exit(WAIT_EXIT_CODE)


def _from_graphjin(url, request, timeout):
    body = {"query": request["query"]}
    if request["variables"]:
        body["variables"] = request["variables"]
    req = urllib.request.Request(url, data=json.dumps(body).encode("utf-8"), method="POST",
                                 headers={"content-type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
    except urllib.error.HTTPError as err:
        raw = err.read()
    except (urllib.error.URLError, OSError) as err:
        raise NekoDataError("GraphJin did not answer: %s" % err)
    try:
        payload = json.loads(raw)
    except ValueError:
        raise NekoDataError("GraphJin returned text that is not JSON: %r" % raw[:300])
    if not isinstance(payload, dict):
        raise NekoDataError("GraphJin returned no JSON object")
    if payload.get("errors"):
        return {"error": json.dumps(payload["errors"])[:4000]}
    return {"data": payload.get("data")}


def query(graphql, variables=None, namespace=None, timeout=300):
    """Run a read query and return its data object. Raises on any GraphQL error."""
    request = _request(graphql, variables, namespace)
    url = os.environ.get("NEKO_DATA_GRAPHJIN_URL")
    answer = _from_graphjin(url, request, timeout) if url else _from_run(request)
    if answer.get("error"):
        raise NekoDataError(answer["error"])
    data = answer.get("data")
    if not isinstance(data, dict):
        raise NekoDataError("the query returned no data object")
    return data
