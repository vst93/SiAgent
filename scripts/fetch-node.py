#!/usr/bin/env python3
"""Install the pinned official Node binary and license into a release directory."""
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tarfile
import tempfile
import urllib.request


def main():
    project = Path(__file__).resolve().parent.parent
    version = (project / ".node-version").read_text().strip()
    platform, architecture, destination = sys.argv[1:]
    node_arch = {"amd64": "x64", "arm64": "arm64"}[architecture]
    name = f"node-v{version}-{platform}-{node_arch}"
    filename = name + ".tar.gz"
    checksums = json.loads((project / "scripts/node-checksums.json").read_text())
    expected = checksums[filename]
    destination = Path(destination)
    with tempfile.TemporaryFile() as archive:
        print(f"Downloading {filename}", flush=True)
        checksum = hashlib.sha256()
        with urllib.request.urlopen(f"https://nodejs.org/dist/v{version}/{filename}", timeout=60) as response:
            while chunk := response.read(1024 * 1024):
                checksum.update(chunk)
                archive.write(chunk)
        if checksum.hexdigest() != expected:
            raise SystemExit(f"SHA-256 mismatch for {filename}")
        archive.seek(0)
        # Copy only the two regular files we need; do not extract archive paths.
        with tarfile.open(fileobj=archive, mode="r:gz") as bundle:
            for source, target in (("bin/node", "libexec/node"), ("LICENSE", "licenses/node-LICENSE")):
                member = bundle.getmember(f"{name}/{source}")
                if not member.isfile():
                    raise SystemExit(f"Unexpected archive member: {source}")
                output = destination / target
                output.parent.mkdir(parents=True, exist_ok=True)
                with bundle.extractfile(member) as stream, output.open("wb") as result:
                    shutil.copyfileobj(stream, result)
                output.chmod(0o755 if source == "bin/node" else 0o644)
    (destination / "NODE_VERSION").write_text(version + "\n")


if __name__ == "__main__":
    main()
