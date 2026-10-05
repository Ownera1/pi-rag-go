#!/usr/bin/env python3
"""Generate a Tap Cask from the complete, verified release checksum manifest."""
import argparse
import json
import re
from pathlib import Path


def generate(version: str, checksums: str) -> str:
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?", version):
        raise ValueError("release version must be vMAJOR.MINOR.PATCH")
    hashes = {}
    for line in checksums.splitlines():
        digest, name = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or name in hashes:
            raise ValueError("invalid or duplicate checksum")
        hashes[name] = digest
    def checksum(os_name, arch):
        return hashes[f"rag-go_{version}_{os_name}_{arch}.tar.gz"]
    return f'''cask "rag-go" do
  version {json.dumps(version[1:])}
  arch arm: "arm64", intel: "amd64"
  name "rag-go"
  desc "Shared local knowledge store with HTTP MCP and automatic PDF ingestion"
  homepage "https://github.com/Ownera1/rag-go"

  on_macos do
    sha256 arm: "{checksum('darwin', 'arm64')}", intel: "{checksum('darwin', 'amd64')}"
    url "https://github.com/Ownera1/rag-go/releases/download/v#{{version}}/rag-go_v#{{version}}_darwin_#{{arch}}.tar.gz"
  end
  on_linux do
    sha256 arm: "{checksum('linux', 'arm64')}", intel: "{checksum('linux', 'amd64')}"
    url "https://github.com/Ownera1/rag-go/releases/download/v#{{version}}/rag-go_v#{{version}}_linux_#{{arch}}.tar.gz"
  end

  binary "rag"
  binary "ragd"
  binary "ragctl"
  binary "ragprep"
  binary "rageval"
end
'''


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("version")
    parser.add_argument("checksums", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    rendered = generate(args.version, args.checksums.read_text())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(rendered)
