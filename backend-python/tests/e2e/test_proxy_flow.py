"""End-to-end tests through the Go proxy (HTTP surface).

Exercises the proxy's test-response interception path and the dashboard REST
endpoints, verifying the proxy -> engine -> result plumbing works without
needing a real upstream LLM key.
"""

import json
import urllib.error
import urllib.request

import pytest

pytestmark = pytest.mark.e2e


def _post_json(url: str, payload: dict, timeout: int = 30):
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url, data=data, headers={"Content-Type": "application/json"}, method="POST"
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, json.loads(resp.read().decode("utf-8"))


def test_test_response_mode_echoes_and_audits(proxy_url):
    """`test_response` mode returns the supplied response (no upstream key) and
    mints a chat-completion id used to correlate the async audit."""
    status, body = _post_json(
        f"{proxy_url}/v1/chat/completions",
        {
            "model": "llama3.2:1b",
            "messages": [{"role": "user", "content": "What is the capital of France?"}],
            "test_response": "Paris is the capital of France.",
        },
    )
    assert status == 200
    assert body["choices"][0]["message"]["content"] == "Paris is the capital of France."
    assert body.get("id"), "expected a chat-completion id"


def test_direct_audit_endpoint_accepts_job(proxy_url):
    status, body = _post_json(
        f"{proxy_url}/api/audit",
        {"query": "What is gravity?", "response": "Gravity is a fundamental force."},
    )
    assert status == 202
    assert body.get("request_id")
    assert body.get("status") == "submitted"


def test_file_upload_ingests_documents(proxy_url):
    """Uploading a JSON claim runs Gate-1 ingestion (LLM decomposition +
    entailment); slow on a CPU judge, hence the long timeout."""
    docs = [{"content": "E2E test document: the sky is blue.", "metadata": {"source": "test"}}]
    file_data = json.dumps(docs).encode("utf-8")
    boundary = "----E2ETestBoundary"
    body = (
        (
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="file"; filename="test.json"\r\n'
            f"Content-Type: application/json\r\n\r\n"
        ).encode("utf-8")
        + file_data
        + f"\r\n--{boundary}--\r\n".encode("utf-8")
    )

    req = urllib.request.Request(
        f"{proxy_url}/api/upload",
        data=body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=180) as resp:
        data = json.loads(resp.read().decode("utf-8"))
    assert data.get("status") == "success"
    assert data.get("documents_ingested", 0) >= 1
