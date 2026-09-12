import time
import os
import pytest
from types import SimpleNamespace

from fastapi.testclient import TestClient

import app as librarian

@pytest.fixture(autouse=True)
def disk_spool(tmp_path_factory, monkeypatch):
    monkeypatch.setattr(librarian.result_store, "ROOT", tmp_path_factory.mktemp("results"))



def test_readiness_requires_every_vendored_model(tmp_path, monkeypatch):
    task_root = tmp_path / "tasks"
    model_root = tmp_path / "models"
    model_root.mkdir()
    monkeypatch.setattr(librarian, "TASK_ROOT", task_root)
    monkeypatch.setattr(librarian, "MODEL_ROOT", model_root)
    with TestClient(librarian.app) as client:
        assert librarian.queue.maxsize == 1
        assert client.get("/health/ready").status_code == 503
        for relative_path in librarian.REQUIRED_MODEL_FILES:
            path = model_root / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        response = client.get("/health/ready")
        assert response.status_code == 200
        assert response.json()["ocr"] is False


def test_rejects_ocr_requests(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        response = client.post(
            "/v1/convert/file/async",
            files={"files": ("digital.pdf", b"%PDF-1.4", "application/pdf")},
            data={"from_formats": "pdf", "do_ocr": "true"},
        )
    assert response.status_code == 400
    assert response.json()["detail"] == "OCR is not supported"


def test_rejects_formats_outside_the_product_contract(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        response = client.post(
            "/v1/convert/file/async",
            files={"files": ("page.html", b"<p>no</p>", "text/html")},
            data={"from_formats": "html", "do_ocr": "false"},
        )
    assert response.status_code == 415


def test_async_result_contract(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    monkeypatch.setattr(librarian, "_convert", lambda _source: "# Digital policy\n\nText")
    with TestClient(librarian.app) as client:
        submitted = client.post(
            "/v1/convert/file/async",
            files={"files": ("digital.pdf", b"%PDF-1.4", "application/pdf")},
            data={"from_formats": "pdf", "do_ocr": "false"},
        )
        assert submitted.status_code == 202
        task_id = submitted.json()["task_id"]
        for _ in range(50):
            polled = client.get(f"/v1/status/poll/{task_id}").json()
            if polled["task_status"] == "success":
                break
            time.sleep(0.01)
        assert polled["task_status"] == "success"
        result = client.get(f"/v1/result/{task_id}")
        assert result.status_code == 200
        assert result.json()["document"]["md_content"].startswith("# Digital")
        assert client.get(f"/v1/status/poll/{task_id}").status_code == 200
        assert client.get(f"/v1/result/{task_id}").json() == result.json()
        assert not librarian.tasks
        assert client.get("/health/idle").json() == {"idle": True}


def test_embedded_text_gate_rejects_picture_and_handwriting_only_documents():
    picture = SimpleNamespace(label="picture")
    handwriting = SimpleNamespace(label="handwritten_text", text="written by hand")
    document = SimpleNamespace(iterate_items=lambda: iter([(picture, 0), (handwriting, 0)]))
    assert librarian._document_has_embedded_text(document) is False


def test_embedded_text_gate_accepts_digital_paragraphs_and_tables():
    paragraph = SimpleNamespace(label="paragraph", text="Digital policy text")
    paragraph_document = SimpleNamespace(iterate_items=lambda: iter([(paragraph, 0)]))
    assert librarian._document_has_embedded_text(paragraph_document) is True

    cell = SimpleNamespace(text="420000")
    table = SimpleNamespace(label="table", data=SimpleNamespace(table_cells=[cell]))
    table_document = SimpleNamespace(iterate_items=lambda: iter([(table, 0)]))
    assert librarian._document_has_embedded_text(table_document) is True
import time


def test_idle_protects_inflight_work_and_unpersisted_failures(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        assert client.get("/health/idle").json() == {"idle": True}
        for state in ("pending", "started", "failure"):
            librarian.tasks["held"] = librarian.Task(
                task_id="held", source=tmp_path / "held.pdf", input_format="pdf", state=state
            )
            assert client.get("/health/idle").json() == {"idle": False}
        librarian.tasks.clear()
        assert client.get("/health/idle").json() == {"idle": True}


def test_admission_includes_uploads_and_inflight_tasks(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        # Invalid multipart would return 422 if parsing ran before admission.
        monkeypatch.setattr(librarian, "admitting", 2)
        assert client.post("/v1/convert/file/async", content=b"invalid").status_code == 429
        monkeypatch.setattr(librarian, "admitting", 0)
        for number in range(2):
            key = str(number)
            librarian.tasks[key] = librarian.Task(key, tmp_path / key, "pdf", state="started")
        assert client.post("/v1/convert/file/async", content=b"invalid").status_code == 429
        librarian.tasks.clear()
        assert client.post("/v1/convert/file/async", content=b"invalid").status_code == 422
        assert librarian.admitting == 0


def test_failure_poll_releases_admission(tmp_path, monkeypatch):
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        librarian.tasks["bad"] = librarian.Task("bad", tmp_path / "bad", "pdf", state="failure", error="invalid PDF")
        response = client.get("/v1/status/poll/bad")
        assert response.json()["task_status"] == "failure"
        assert response.json()["error_message"] == "invalid PDF"
        assert not librarian.tasks


def test_completed_spool_does_not_hold_processing_slots(tmp_path, monkeypatch):
    from uuid import uuid4
    store = librarian.result_store
    ids = [str(uuid4()) for _ in range(3)]
    for task_id in ids:
        store.save(task_id, "# Stored")
    monkeypatch.setattr(librarian, "TASK_ROOT", tmp_path)
    with TestClient(librarian.app) as client:
        assert client.get("/health/idle").json() == {"idle": True}
        # Parsing runs: cached results did not cause admission to return 429.
        assert client.post("/v1/convert/file/async", content=b"invalid").status_code == 422
        assert client.get(f"/v1/result/{ids[0]}").json()["document"]["md_content"] == "# Stored"
    with TestClient(librarian.app) as client:
        assert client.get(f"/v1/result/{ids[0]}").status_code == 200
    monkeypatch.setattr(store, "MAX_BYTES", store.usage()[1])
    assert not store.has_capacity(0)
    with TestClient(librarian.app) as client:
        assert client.post("/v1/convert/file/async", content=b"invalid").status_code == 429
    with pytest.raises(OSError):
        store.save(str(uuid4()), "too full")
    folder = store.ROOT / ids[0]
    old = time.time() - store.TTL - 1
    os.utime(folder, (old, old))
    assert store.read(ids[0], "result.json") is None
    store.usage()
    assert not folder.exists()
    assert store.read("../../outside", "result.json") is None


def test_oversized_results_never_publish_partial_files(monkeypatch):
    from uuid import uuid4
    store = librarian.result_store
    monkeypatch.setattr(store, "MAX_RESULT_BYTES", 128)
    task_id = str(uuid4())
    with pytest.raises(ValueError):
        store.save(task_id, "x" * 129)
    assert not (store.ROOT / task_id).exists()
    assert not list(store.ROOT.glob(".tmp-*"))
