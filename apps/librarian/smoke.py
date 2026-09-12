"""Offline image smoke: real PDF extraction across lazy child eviction."""
import json
import os
import subprocess
import time
import urllib.request

BASE = "http://127.0.0.1:5001"
PHRASE = "OpenNeko lazy document extraction"


def request(path, data=None, content_type="application/json"):
    req = urllib.request.Request(BASE + path, data=data)
    req.add_header("Content-Type", content_type)
    with urllib.request.urlopen(req, timeout=150) as response:
        return json.load(response)


def until(check, timeout=150):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            value = check()
            if value:
                return value
        except OSError:
            pass
        time.sleep(0.5)
    raise AssertionError("service state timed out")


def pdf():
    stream = f"BT /F1 16 Tf 72 720 Td ({PHRASE}) Tj ET".encode()
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    output, offsets = bytearray(b"%PDF-1.4\n"), []
    for number, body in enumerate(objects, 1):
        offsets.append(len(output))
        output.extend(f"{number} 0 obj\n".encode() + body + b"\nendobj\n")
    xref = len(output)
    output.extend(f"xref\n0 {len(objects)+1}\n0000000000 65535 f \n".encode())
    for offset in offsets:
        output.extend(f"{offset:010d} 00000 n \n".encode())
    output.extend(f"trailer\n<< /Size {len(objects)+1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(output)


def submit():
    boundary = "openneko-lazy-smoke"
    parts = [f'--{boundary}\r\nContent-Disposition: form-data; name="files"; filename="digital.pdf"\r\nContent-Type: application/pdf\r\n\r\n'.encode() + pdf() + b"\r\n"]
    for name, value in [("from_formats", "pdf"), ("do_ocr", "false")]:
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
    parts.append(f"--{boundary}--\r\n".encode())
    return request("/v1/convert/file/async", b"".join(parts), f"multipart/form-data; boundary={boundary}")["task_id"]


if __name__ == "__main__":
    child = subprocess.Popen(["lazy-service", "--", "uvicorn", "app:app", "--no-access-log"], env={**os.environ, "OPENNEKO_SERVICE_IDLE_TIMEOUT": "1s"})
    try:
        until(lambda: request("/health/ready")["state"] == "sleeping")
        assert request("/health/ready")["starts"] == 0
        for generation in (1, 2):
            task_id = submit()
            def finished():
                state = request(f"/v1/status/poll/{task_id}")["task_status"]
                assert state != "failure", request(f"/v1/result/{task_id}")
                return state == "success"
            until(finished)
            # Uncollected results must not retain the processing child.
            until(lambda: request("/health/ready")["state"] == "sleeping")
            result = request(f"/v1/result/{task_id}")
            assert PHRASE in result["document"]["md_content"]
            until(lambda: request("/health/ready")["state"] == "sleeping")
            assert request("/health/ready")["starts"] == generation
        child.terminate()
        child.wait(timeout=15)
        child = subprocess.Popen(["lazy-service", "--", "uvicorn", "app:app", "--no-access-log"], env={**os.environ, "OPENNEKO_SERVICE_IDLE_TIMEOUT": "1s"})
        until(lambda: request("/health/ready")["starts"] == 0)
        assert PHRASE in request(f"/v1/result/{task_id}")["document"]["md_content"]
        assert request(f"/v1/status/poll/{task_id}")["task_status"] == "success"
        assert request("/health/ready")["starts"] == 0
        print("librarian_offline_disk_spool_restart=ok")
    finally:
        child.terminate()
        child.wait(timeout=15)
