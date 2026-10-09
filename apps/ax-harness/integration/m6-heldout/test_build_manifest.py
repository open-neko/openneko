"""Guard the source label on reviewed held-out receipts."""

import hashlib
import importlib.util
import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("heldout_manifest", HERE / "build-manifest.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ManifestSourceTest(unittest.TestCase):
    def test_synthetic_receipts_cannot_be_relabelled_live(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            checkpoint_root = root / "checkpoints"
            checkpoint_root.mkdir()
            environment = {
                "provider": "openai-compatible", "model": "graphjin-fixture",
                "reasoning": "high", "eval_fingerprint": "a" * 64,
                "data_snapshot_sha256": "b" * 64,
            }
            review = {"version": 1, "cases": {}}
            for case_id in MODULE.CASES:
                review["cases"][case_id] = {
                    "task_class": "artifact" if case_id.endswith("artifact-001") else "short",
                    "runs": {},
                }
                for mode in MODULE.MODES:
                    run_id = f"{case_id}-{mode}"
                    checkpoint = checkpoint_root / (hashlib.sha256(run_id.encode()).hexdigest() + ".json")
                    checkpoint.write_text("{}", encoding="utf-8")
                    data = {
                        "version": 1, "dataset": "daily-lead-union-v1",
                        "cases_sha256": MODULE.SHA256, "case_id": case_id,
                        "mode": mode, "source": "synthetic", "model_name": "harness-fixture",
                        "root": str(checkpoint_root), "run_id": run_id,
                        "checkpoint_sha256": hashlib.sha256(checkpoint.read_bytes()).hexdigest(),
                        "graphjin_environment": environment, "wall_ms": 1,
                        "api_status": "completed", "checkpoint_status": "completed",
                    }
                    (root / f"{run_id}.json").write_text(json.dumps(data), encoding="utf-8")
                    review["cases"][case_id]["runs"][mode] = {
                        "reviewed": True, "outcome": "verified_failure",
                    }
            manifest = MODULE.build(root, review, "synthetic")
            self.assertEqual({pair["source"] for pair in manifest["pairs"]}, {"synthetic"})
            with self.assertRaisesRegex(ValueError, "invalid frozen receipt identity"):
                MODULE.build(root, review, "live")
            first = root / f"{MODULE.CASES[0]}-fixed.json"
            data = json.loads(first.read_text(encoding="utf-8"))
            data["source"] = "live"
            first.write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "fixture profile"):
                MODULE.receipt(first, MODULE.CASES[0], "fixed", "live")


if __name__ == "__main__":
    unittest.main()
