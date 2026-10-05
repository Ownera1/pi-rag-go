#!/usr/bin/env python3
"""Exercise extracted rag with CLI, stdio MCP, HTTP MCP and a local model stub."""
import argparse
import json
import select
import subprocess
import tempfile
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class Provider(BaseHTTPRequestHandler):
    calls = 0

    def log_message(self, *args):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path.endswith("/rerank"):
            result = {"results": [{"index": i, "relevance_score": 1.0 - i / 100}
                                  for i in range(min(len(data["documents"]), data["top_n"]))]}
        else:
            Provider.calls += 1
            inputs = data["input"]
            result = {"data": [{"index": i, "embedding": [1.0, 0.0]} for i in range(len(inputs))]}
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(result).encode())


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()


def stdio_call(process, identifier, method, params, expect_error=False):
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": identifier,
                                   "method": method, "params": params}) + "\n")
    process.stdin.flush()
    if not select.select([process.stdout], [], [], 15)[0]:
        raise TimeoutError(method)
    response = json.loads(process.stdout.readline())
    assert response["id"] == identifier, response
    if expect_error:
        assert response.get("error") or response.get("result", {}).get("isError"), response
        return response
    assert "error" not in response, response
    return response["result"]


def check_stdio(binary, root, readonly):
    args = [str(binary), "mcp", "--workspace", str(root)]
    if readonly:
        args.append("--read-only")
    process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True)
    try:
        stdio_call(process, 1, "initialize", {"protocolVersion": "2025-03-26",
                   "capabilities": {}, "clientInfo": {"name": "release-smoke", "version": "1"}})
        process.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        process.stdin.flush()
        tools = stdio_call(process, 2, "tools/list", {})["tools"]
        names = sorted(tool["name"] for tool in tools)
        expected = ["rag_list_documents", "rag_query", "rag_status"]
        if not readonly:
            expected += ["rag_sync", "rag_rebuild"]
        assert names == sorted(expected), names
        query_tool = next(t for t in tools if t["name"] == "rag_query")
        assert query_tool.get("annotations", {}).get("readOnlyHint", False) == readonly
        result = stdio_call(process, 3, "tools/call", {"name": "rag_query",
                             "arguments": {"query": "pendingmarker", "mode": "bm25"}})
        assert not result.get("isError"), result
        structured = result["structuredContent"]
        assert bool(structured["hits"]) == (not readonly), structured
        if readonly:
            stdio_call(process, 4, "tools/call", {"name": "rag_sync", "arguments": {}}, expect_error=True)
    finally:
        stop(process)


def http_call(url, identifier, method, params, session=None):
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if session:
        headers["Mcp-Session-Id"] = session
        headers["MCP-Protocol-Version"] = "2025-03-26"
    body = json.dumps({"jsonrpc": "2.0", "id": identifier, "method": method, "params": params}).encode()
    with urllib.request.urlopen(urllib.request.Request(url, body, headers), timeout=15) as response:
        raw = response.read().decode()
        if raw.startswith("event:") or raw.startswith("data:"):
            raw = next(line[6:] for line in raw.splitlines() if line.startswith("data: "))
        result = json.loads(raw)
        return result.get("result"), response.headers.get("Mcp-Session-Id", session), result.get("error")


def smoke(binaries):
    binary = binaries.resolve() / "rag"
    provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="rag-release-") as scratch:
            root = Path(scratch) / "workspace with spaces"
            base_url = f"http://127.0.0.1:{provider.server_port}"

            def cli(*args):
                result = subprocess.run([str(binary), *args, "--workspace", str(root)],
                                        check=True, capture_output=True, text=True, timeout=30)
                return json.loads(result.stdout)

            cli("init", "--embedding-type", "openai", "--model", "smoke",
                "--dimensions", "2", "--base-url", base_url, "--api-key-env=")
            config_path = root / ".rag-go/config.json"
            config = json.loads(config_path.read_text())
            config["chunking"]["mode"] = "legacy"
            config["reranker"] = {"type": "http", "model": "smoke", "baseUrl": base_url}
            config_path.write_text(json.dumps(config))
            docs = root / "documents"
            (docs / "first.txt").write_text("release channel estimation evidence")
            assert cli("sync")["indexed"] == 1
            count = Provider.calls
            assert cli("sync")["skipped"] == 1 and Provider.calls == count
            for mode in ("bm25", "vector", "hybrid"):
                result = cli("query", "channel estimation", "--mode", mode)
                assert result["hits"] and result["freshness"] == "fresh", result
            (docs / "second.txt").write_text("pendingmarker evidence")
            before = Provider.calls
            check_stdio(binary, root, True)
            assert Provider.calls == before
            check_stdio(binary, root, False)
            assert cli("status")["files"] == 2
            paper = docs / "paper"
            paper.mkdir()
            (paper / "paper_content_list.json").write_text(json.dumps([
                {"type": "text", "text": "provenance evidence", "page_idx": 4}]))
            (paper / "full.md").write_text("duplicate_leak evidence")
            result = cli("query", "provenance", "--mode", "bm25")
            assert result["hits"][0]["chunk"]["pageStart"] == 5
            assert not cli("query", "duplicate_leak", "--mode", "bm25")["hits"]
            (docs / "second.txt").unlink()
            assert cli("sync")["removed"] == 1
            # HTTP always enforces read-only, including an explicit false stdio flag.
            (docs / "http-pending.txt").write_text("httppending evidence")
            process = subprocess.Popen([str(binary), "mcp", "--transport", "http", "--read-only=false",
                                       "--workspace", str(root)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                assert select.select([process.stderr], [], [], 10)[0], "HTTP startup timeout"
                line = process.stderr.readline().strip()
                url = line.split("MCP: ", 1)[1]
                _, session, error = http_call(url, 1, "initialize", {"protocolVersion": "2025-03-26",
                    "capabilities": {}, "clientInfo": {"name": "http-smoke", "version": "1"}})
                assert not error
                tools, _, error = http_call(url, 2, "tools/list", {}, session)
                assert not error and sorted(t["name"] for t in tools["tools"]) == ["rag_list_documents", "rag_query", "rag_status"]
                result, _, error = http_call(url, 3, "tools/call", {"name": "rag_query",
                    "arguments": {"query": "httppending", "mode": "bm25"}}, session)
                assert not error and not result.get("isError") and not result["structuredContent"]["hits"]
                assert cli("status")["files"] == 2
                result, _, error = http_call(url, 4, "tools/call", {"name": "rag_rebuild", "arguments": {}}, session)
                assert error or result.get("isError")
            finally:
                stop(process)
                assert not process.stdout.read(), "HTTP diagnostics leaked to stdout"
            cli("sync")
            for _ in range(3):
                assert cli("rebuild")["failed"] == 0
            assert cli("clean", "--keep", "1")["dryRun"]
            assert len(cli("clean", "--keep", "1", "--confirm")["removed"]) == 2
            dataset = root / "questions.jsonl"
            dataset.write_text(json.dumps({"id": "one", "query": "channel estimation",
                               "relevant": [{"pathSuffix": "first.txt", "contains": "channel estimation"}]}) + "\n")
            report = cli("eval", "--dataset", str(dataset), "--modes", "bm25,vector,hybrid,rerank")
            assert all(s["failed"] == 0 and s["recallAtK"] == 1 for s in report["summaries"])
            print("Release smoke passed: standalone CLI, auto sync, FTS5/sqlite-vec, provenance, stdio MCP, read-only HTTP, rebuild/clean, four-mode evaluation.")
    finally:
        provider.shutdown()
        provider.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("binary_directory", type=Path)
    smoke(parser.parse_args().binary_directory)
