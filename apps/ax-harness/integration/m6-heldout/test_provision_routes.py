"""Guard the native Typesafe operation path used by the live M6 gate."""

import importlib.util
import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "m6_routes", Path(__file__).with_name("provision-routes.py"))
ROUTES = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ROUTES)


class TypesafeRouteTest(unittest.TestCase):
    def test_native_base_url_allows_ax_operation(self):
        base, allowed = ROUTES.typesafe_endpoint("https://api.typesafe.ai/")
        self.assertEqual(base, "https://api.typesafe.ai")
        self.assertEqual(base + "/v1/systemone", "https://api.typesafe.ai/v1/systemone")
        self.assertEqual(allowed["path"], "/v1/**")

        base, allowed = ROUTES.typesafe_endpoint("https://proxy.example.test/triage")
        self.assertEqual(base + "/v1/systemone", "https://proxy.example.test/triage/v1/systemone")
        self.assertEqual(allowed["path"], "/triage/v1/**")

    def test_prevent_duplicate_operation_path(self):
        for url in ("https://api.typesafe.ai/v1", "https://api.typesafe.ai/v1/systemone"):
            with self.subTest(url=url), self.assertRaisesRegex(ValueError, "must omit"):
                ROUTES.typesafe_endpoint(url)

    def test_generated_route_and_profile_agree(self):
        with TemporaryDirectory() as directory:
            args = ["provision-routes.py", "--output-dir", directory,
                    "--model-url", "https://generativelanguage.googleapis.com/v1beta/openai",
                    "--model-name", "gemini-3.8-flash",
                    "--triage-url", "https://api.typesafe.ai", "--triage-model", "jev-latest"]
            for name in ("model", "triage", "graphjin"):
                args.extend((f"--{name}-input-price", "1000000",
                             f"--{name}-output-price", "1000000"))
            with patch("sys.argv", args):
                ROUTES.main()
            root = Path(directory)
            routing = json.loads((root / "routing.json").read_text())
            profile = json.loads((root / "triage-provider.json").read_text())
            self.assertEqual(routing["routes"][1]["url"] + "/v1/systemone",
                             "https://api.typesafe.ai/v1/systemone")
            self.assertEqual(profile["endpoints"][0]["path"], "/v1/**")


if __name__ == "__main__":
    unittest.main()
