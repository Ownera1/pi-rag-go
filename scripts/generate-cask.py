#!/usr/bin/env python3
"""Generate Tap packages from the complete, verified release checksum manifest."""
import argparse
import json
import re
from pathlib import Path


def read_checksums(version: str, checksums: str) -> dict:
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?", version):
        raise ValueError("release version must be vMAJOR.MINOR.PATCH")
    hashes = {}
    for line in checksums.splitlines():
        digest, name = line.split()
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or name in hashes:
            raise ValueError("invalid or duplicate checksum")
        hashes[name] = digest
    for os_name in ("darwin", "linux"):
        for arch in ("arm64", "amd64"):
            hashes[f"rag-go_{version}_{os_name}_{arch}.tar.gz"]
    return hashes


def generate(version: str, checksums: str) -> str:
    hashes = read_checksums(version, checksums)
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

  caveats <<~EOS
    For these CLI binaries, use the precompiled Formula:
      brew uninstall --cask ownera1/tap/rag-go
      brew install --formula ownera1/tap/rag-go
    The Cask's quarantine may prevent these ad-hoc-signed binaries from running.
  EOS
end
'''


def generate_formula(version: str, checksums: str) -> str:
    hashes = read_checksums(version, checksums)
    stanzas = []
    for os_name, ruby_os in (("darwin", "macos"), ("linux", "linux")):
        platforms = []
        for arch, ruby_arch in (("arm64", "arm"), ("amd64", "intel")):
            name = f"rag-go_{version}_{os_name}_{arch}.tar.gz"
            platforms.append(f'''    on_{ruby_arch} do
      url "https://github.com/Ownera1/rag-go/releases/download/{version}/{name}?package=formula"
      sha256 "{hashes[name]}"
    end''')
        stanzas.append(f"  on_{ruby_os} do\n" + "\n".join(platforms) + "\n  end")
    return f'''class RagGo < Formula
  desc "Shared local knowledge store with HTTP MCP and automatic PDF ingestion"
  homepage "https://github.com/Ownera1/rag-go"
  version {json.dumps(version[1:])}
  license "MIT"

{chr(10).join(stanzas)}

  def install
    bin.install "rag", "ragd", "ragctl", "ragprep", "rageval"
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
    parser.add_argument("--formula", action="store_true", help="generate the precompiled CLI Formula")
    args = parser.parse_args()
    generator = generate_formula if args.formula else generate
    rendered = generator(args.version, args.checksums.read_text())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(rendered)
