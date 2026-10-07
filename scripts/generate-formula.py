#!/usr/bin/env python3
"""Generate Tap packages from the complete, verified release checksum manifest."""
import argparse
import json
import re
from pathlib import Path

PLATFORMS = (("darwin", "arm64"), ("linux", "arm64"), ("linux", "amd64"))


def read_checksums(version: str, checksums: str) -> dict:
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?", version):
        raise ValueError("release version must be vMAJOR.MINOR.PATCH")
    hashes = {}
    for line in checksums.splitlines():
        digest, name = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or name in hashes:
            raise ValueError("invalid or duplicate checksum")
        hashes[name] = digest
    for os_name, arch in PLATFORMS:
        hashes[f"rag-go_{version}_{os_name}_{arch}.tar.gz"]
    return hashes


def generate_formula(version: str, checksums: str) -> str:
    hashes = read_checksums(version, checksums)
    stanzas = []
    for os_name, ruby_os in (("darwin", "macos"), ("linux", "linux")):
        platforms = []
        for arch, ruby_arch in (("arm64", "arm"), ("amd64", "intel")):
            if (os_name, arch) not in PLATFORMS:
                continue
            name = f"rag-go_{version}_{os_name}_{arch}.tar.gz"
            platforms.append(f'''    on_{ruby_arch} do
      url "https://github.com/Ownera1/rag-go/releases/download/{version}/{name}?package=formula"
      sha256 "{hashes[name]}"
    end''')
        stanzas.append(f"  on_{ruby_os} do\n" + "\n".join(platforms) + "\n  end")
    return f'''class RagGo < Formula
  desc "Workspace local retrieval engine with stdio MCP"
  homepage "https://github.com/Ownera1/rag-go"
  version {json.dumps(version[1:])}
  license "MIT"

{chr(10).join(stanzas)}

  def install
    bin.install "rag"
  end

  test do
    assert_match version.to_s, shell_output("#{{bin}}/rag version")
  end
end
'''


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("version")
    parser.add_argument("checksums", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    rendered = generate_formula(args.version, args.checksums.read_text())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(rendered)
