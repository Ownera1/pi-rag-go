#!/usr/bin/env python3
"""Validate Formula completeness and installer verification without network access."""
import hashlib
import importlib.util
import io
import os
import platform
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("formula", ROOT / "scripts/generate-formula.py")
formula = importlib.util.module_from_spec(spec)
spec.loader.exec_module(formula)


class ReleaseTests(unittest.TestCase):
    def test_formula_requires_complete_native_archives(self):
        checksums = "\n".join(f"{'b' * 64}  rag-go_v1.2.3_{os_name}_{arch}.tar.gz"
                              for os_name in ("darwin", "linux") for arch in ("arm64", "amd64"))
        rendered = formula.generate_formula("v1.2.3", checksums)
        self.assertEqual(rendered.count('      url "'), 4)
        self.assertEqual(rendered.count('?package=formula"'), 4)
        self.assertIn('bin.install "rag"', rendered)
        self.assertNotIn('depends_on "go"', rendered)
        with self.assertRaises(KeyError):
            formula.generate_formula("v1.2.3", checksums.splitlines()[0])

    def test_installer_checks_checksum_and_preserves_existing_installation(self):
        with tempfile.TemporaryDirectory(prefix="rag-installer-") as scratch:
            base = Path(scratch)
            mirror, tools, install = base / "mirror", base / "tools", base / "install"
            mirror.mkdir(); tools.mkdir(); install.mkdir()
            os_name = "darwin" if platform.system() == "Darwin" else "linux"
            arch = "arm64" if platform.machine() in ("arm64", "aarch64") else "amd64"
            archive = f"rag-go_v1.2.3_{os_name}_{arch}.tar.gz"
            with tarfile.open(mirror / archive, "w:gz") as bundle:
                for name in ("rag",):
                    content = b"#!/bin/sh\necho fixture\n"
                    info = tarfile.TarInfo(name); info.size = len(content); info.mode = 0o755
                    bundle.addfile(info, io.BytesIO(content))
            checksum = hashlib.sha256((mirror / archive).read_bytes()).hexdigest()
            (mirror / "checksums.txt").write_text(f"{checksum}  {archive}\n")
            curl = tools / "curl"
            curl.write_text('#!/bin/sh\nsource=""\ndestination=""\nwhile [ "$#" -gt 0 ]; do\ncase "$1" in https:*) source=${1##*/}; shift;; --output) destination=$2; shift 2;; *) shift;; esac\ndone\ncp "$RAG_INSTALL_TEST_MIRROR/$source" "$destination"\n')
            curl.chmod(0o755)
            env = dict(os.environ, PATH=f"{tools}:{os.environ['PATH']}", RAG_INSTALL_TEST_MIRROR=str(mirror))
            command = ["sh", str(ROOT / "scripts/install.sh"), "--version", "v1.2.3", "--prefix", str(install)]
            subprocess.run(command, env=env, check=True, capture_output=True)
            before = (install / "rag").read_bytes()
            (mirror / "checksums.txt").write_text(f"{'0' * 64}  {archive}\n")
            failed = subprocess.run(command, env=env, capture_output=True)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn(b"Checksum mismatch", failed.stderr)
            self.assertEqual((install / "rag").read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
