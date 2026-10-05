#!/usr/bin/env python3
"""Exercise installed binaries with a deterministic local embedding provider."""
import argparse
import json
import os
import socket
import shutil
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        inputs = data["input"]
        if isinstance(inputs, str):
            inputs = [inputs]
        result = {"data": [{"index": i, "embedding": [1.0, 0.0]} for i in range(len(inputs))]}
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(result).encode())


def pdf_fixture(text: str) -> bytes:
    stream = f"BT /F1 12 Tf 72 720 Td ({text}) Tj ET\n"
    objects = ["<< /Type /Catalog /Pages 2 0 R >>",
               "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
               "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
               "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
               f"<< /Length {len(stream)} >>\nstream\n{stream}endstream"]
    data = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for i, obj in enumerate(objects, 1):
        offsets.append(len(data))
        data.extend(f"{i} 0 obj\n{obj}\nendobj\n".encode())
    xref = len(data)
    data.extend(f"xref\n0 {len(offsets)}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        data.extend(f"{offset:010d} 00000 n \n".encode())
    data.extend(f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(data)


def smoke(binaries: Path, persistent: Path | None = None):
    binaries = binaries.resolve()
    provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="rag-release-") as scratch:
        base = persistent or Path(scratch)
        base.mkdir(parents=True, exist_ok=True)
        store, source = base / "store", base / "papers"
        source.mkdir(exist_ok=True)
        store.mkdir(exist_ok=True)
        config = {
            "embedding": {"type": "openai", "model": "smoke", "dimensions": 2,
                          "baseUrl": f"http://127.0.0.1:{provider.server_port}", "apiKeyEnv": ""},
            "chunking": {"mode": "legacy"},
            "runtime": {"listen": f"127.0.0.1:{port}", "autoRefresh": {
                "enabled": True, "debounceMs": 100, "rescanMs": 500}},
        }
        converter = shutil.which("pdftotext")
        if not converter:
            raise RuntimeError("Poppler is required for the release PDF acceptance test")
        config["runtime"]["pdf"] = {"backend": "pdftotext", "command": converter, "timeoutMs": 10000}
        (store / "config.json").write_text(json.dumps(config))
        log = open(base / "daemon.log", "w")
        process = subprocess.Popen([str(binaries / "rag"), "serve", "--store", str(store)], stdout=log, stderr=log)
        def cli(*args, legacy=False):
            command = [str(binaries / ("ragctl" if legacy else "rag"))]
            if legacy:
                command += ["--endpoint", f"http://127.0.0.1:{port}/mcp", *args]
            else:
                command += [*args, "--store", str(store)]
            return json.loads(subprocess.check_output(command, stderr=subprocess.PIPE, timeout=20))
        def wait(condition):
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("daemon exited: " + (base / "daemon.log").read_text())
                try:
                    if condition():
                        return
                except subprocess.CalledProcessError:
                    pass
                time.sleep(0.1)
            raise RuntimeError("smoke condition did not settle")
        try:
            wait(lambda: cli("status")["storeDir"] == str(store))
            first = source / "first.txt"
            first.write_text("release smoke channel estimation evidence")
            assert cli("add", str(source))["failed"] == 0
            assert cli("query", "channel estimation", "--mode", "bm25")["hits"]
            assert cli("query", "channel estimation", "--mode", "vector")["hits"]
            assert cli("status", legacy=True)["files"] == 1
            pdf = source / "automatic.pdf"
            pdf.write_bytes(pdf_fixture("publication provenance distinctive evidence"))
            wait(lambda: cli("status")["files"] == 2)
            hits = cli("query", "publication provenance", "--mode", "bm25")["hits"]
            assert hits and hits[0]["chunk"]["sourcePath"] == str(pdf)
            assert hits[0]["chunk"]["pageStart"] == 1
            pdf.unlink()
            wait(lambda: cli("status")["files"] == 1)
            second = source / "second.txt"
            second.write_text("automatic update unique evidence")
            wait(lambda: cli("status")["files"] == 2)
            second.unlink()
            wait(lambda: cli("status")["files"] == 1)
            assert cli("remove", str(source))["removedDocuments"] == 1
            assert cli("status")["files"] == 0
            assert first.exists()
            print("Release smoke passed: FTS5, sqlite-vec, legacy CLI, automatic real PDF conversion/provenance/deletion, watcher add/delete, remove.")
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            log.close()
            provider.shutdown()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("binary_directory", type=Path)
    smoke(parser.parse_args().binary_directory)
