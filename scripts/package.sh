#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
platform=$(go env GOOS)
architecture=$(go env GOARCH)
host_platform=$(go env GOHOSTOS)
host_architecture=$(go env GOHOSTARCH)
if [ "$platform/$architecture" != "$host_platform/$host_architecture" ]; then
  echo 'Build the release on the target OS and architecture so npm dependencies match the executable.' >&2
  exit 1
fi

case "$platform/$architecture" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo "Unsupported release target: $platform/$architecture" >&2; exit 1 ;;
esac

mkdir -p artifacts
staging=$(mktemp -d "$project_dir/artifacts/.package.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
name="siagent-$platform-$architecture"
release_dir="$staging/$name"
mkdir -p "$release_dir/runtime"
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$release_dir/siagent" ./cmd/siagent
cp -R runtime/dist "$release_dir/runtime/dist"
cp runtime/package.json runtime/package-lock.json "$release_dir/runtime/"
python3 scripts/fetch-node.py "$platform" "$architecture" "$release_dir"
# Use the bundled Node for production install; never prune developer dependencies.
PATH="$release_dir/libexec:$PATH" npm --prefix "$release_dir/runtime" ci --omit=dev
"$release_dir/libexec/node" -e 'require("node:sqlite")'
cp LICENSE "$release_dir/LICENSE"
cp docs/server-package.md "$release_dir/README.md"
# Keep executable JS, assets and licenses; omit type declarations and source maps.
python3 - "$release_dir" "artifacts/$name.tar.xz" <<'PY'
from pathlib import Path
import sys
import tarfile

release = Path(sys.argv[1])
archive = Path(sys.argv[2])
removed = 0
suffixes = (".d.ts", ".d.cts", ".d.mts", ".js.map", ".mjs.map", ".cjs.map",
            ".d.ts.map", ".d.cts.map", ".d.mts.map")
for path in (release / "runtime/node_modules").rglob("*"):
    if path.is_file() and not path.is_symlink() and path.name.endswith(suffixes):
        removed += path.stat().st_size
        path.unlink()
with tarfile.open(archive, "w:xz", preset=6) as bundle:
    bundle.add(release, arcname=release.name)
print(f"Omitted declarations/source maps: {removed / 1024**2:.2f} MiB")
print(f"Release archive: {archive.stat().st_size / 1024**2:.2f} MiB")
PY
printf 'Release package: %s/artifacts/%s.tar.xz\n' "$project_dir" "$name"
