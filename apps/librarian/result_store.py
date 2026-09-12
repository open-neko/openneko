"""Bounded disk spool shared with the lightweight Go result reader."""
import json
import os
from pathlib import Path
import shutil
import time
from uuid import UUID

ROOT = Path(os.environ.get("NEKO_LIBRARIAN_RESULT_ROOT", "/var/lib/neko-librarian/results"))
TTL = int(os.environ.get("NEKO_LIBRARIAN_RESULT_TTL_SECONDS", "900"))
MAX_BYTES = int(os.environ.get("NEKO_LIBRARIAN_RESULT_MAX_BYTES", str(512 * 1024 * 1024)))
MAX_RESULTS = 256
MAX_RESULT_BYTES = 8 * 1024 * 1024
if TTL <= 0 or MAX_BYTES < MAX_RESULT_BYTES:
    raise ValueError("result retention must be positive and quota at least 8 MiB")


def usage():
    ROOT.mkdir(parents=True, exist_ok=True)
    count, size = 0, 0
    for folder in ROOT.iterdir():
        if not folder.is_dir():
            continue
        # The listener also expires files while the processing child sleeps.
        try:
            if time.time() - folder.stat().st_mtime >= TTL:
                shutil.rmtree(folder)
                continue
            files = list(folder.iterdir())
            size += sum(p.stat().st_size for p in files if p.is_file())
            count += 1
        except FileNotFoundError:
            pass  # concurrent listener expiry
    return count, size


def has_capacity(active):
    count, size = usage()
    # Reserve the worst-case result size for each admitted conversion.
    return count + active < MAX_RESULTS and size + (active + 1) * MAX_RESULT_BYTES <= MAX_BYTES


def save(task_id, markdown=None, error=None):
    state = "failure" if error is not None else "success"
    status = {"task_id": task_id, "task_status": state, "error_message": error}
    result = {"status": state, "document": {"md_content": markdown} if error is None else None,
              "errors": [{"error_message": error}] if error is not None else []}
    encoded = {"status.json": json.dumps(status).encode(), "result.json": json.dumps(result).encode()}
    if sum(map(len, encoded.values())) > MAX_RESULT_BYTES:
        raise ValueError("extraction result exceeds 8 MiB")
    count, size = usage()
    if count >= MAX_RESULTS or size + sum(map(len, encoded.values())) > MAX_BYTES:
        raise OSError("extraction result disk quota is full")
    temporary = ROOT / (".tmp-" + task_id)
    temporary.mkdir(mode=0o700)
    try:
        for name, body in encoded.items():
            with (temporary / name).open("xb") as output:
                output.write(body)
                output.flush()
                os.fsync(output.fileno())
        directory = os.open(temporary, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
        temporary.rename(ROOT / task_id)
        directory = os.open(ROOT, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        shutil.rmtree(temporary, ignore_errors=True)


def read(task_id, filename):
    try:
        if str(UUID(task_id)) != task_id:
            return None
        folder = ROOT / task_id
        if time.time() - folder.stat().st_mtime >= TTL:
            return None
        return json.loads((folder / filename).read_text())
    except (ValueError, FileNotFoundError):
        return None
