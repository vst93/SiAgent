#!/bin/sh
set -eu

fail() { printf 'SiAgent: %s\n' "$*" >&2; exit 1; }
version=latest
repository=vst93/SiAgent
prefix=${XDG_DATA_HOME:-$HOME/.local/share}/siagent
bin_dir=$HOME/.local/bin
modify_path=yes
service=yes
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--repo|--prefix|--bin-dir)
      [ "$#" -ge 2 ] || fail "Missing value for $1"
      case "$1" in
        --version) version=$2 ;; --repo) repository=$2 ;;
        --prefix) prefix=$2 ;; --bin-dir) bin_dir=$2 ;;
      esac
      shift 2 ;;
    --no-modify-path) modify_path=no; shift ;;
    --no-service) service=no; shift ;;
    -h|--help)
      echo 'Usage: install.sh [--version v0.1.0] [--repo owner/repo] [--prefix DIR] [--bin-dir DIR] [--no-modify-path] [--no-service]'
      exit 0 ;;
    *) fail "Unknown option: $1" ;;
  esac
done
case "$repository" in *[!a-zA-Z0-9_./-]*|*..*|/*|*/) fail 'Invalid GitHub repository' ;; esac
case "$repository" in */*) ;; *) fail 'Repository must be owner/repo' ;; esac
case "${repository#*/}" in */*) fail 'Repository must be owner/repo' ;; esac
for tool in curl tar awk sed mktemp; do command -v "$tool" >/dev/null 2>&1 || fail "Required command missing: $tool"; done
case "$(uname -s)" in
  Linux) platform=linux ;;
  Darwin) platform=darwin ;;
  *) fail 'Use install.ps1 on Windows; this script supports Linux and macOS.' ;;
esac
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) fail 'Only amd64 and arm64 are supported.' ;;
esac
if [ "$platform" = linux ]; then
  command -v xz >/dev/null 2>&1 || fail 'Install xz-utils (or the xz package) before extracting .tar.xz releases.'
  if command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | awk '/musl/ {found=1} END {exit !found}'; then
    fail 'This release requires glibc; Alpine/musl is not supported.'
  fi
fi
if command -v sha256sum >/dev/null 2>&1; then hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then hash_tool=shasum
else fail 'Install sha256sum or shasum to verify the release.'
fi
# Official GitHub and the configured public mirror prefixes race once; the
# fastest reachable source serves every later download. Mirrors only wrap
# github.com URLs. Set SIAGENT_GITHUB_MIRRORS='' to disable fallback, or use
# comma-separated HTTPS prefixes.
release_base=${SIAGENT_RELEASE_BASE_URL:-https://github.com/$repository/releases}
release_base=${release_base%/}
case "$release_base" in
  *\ *) fail 'SIAGENT_RELEASE_BASE_URL must not contain spaces' ;;
esac
sources=$release_base
case "$release_base" in
  https://github.com/*)
    mirrors=${SIAGENT_GITHUB_MIRRORS-'https://ghproxy.net/,https://gh-proxy.com/'}
    remaining=$mirrors
    while [ -n "$remaining" ]; do
      mirror=${remaining%%,*}
      case "$remaining" in *,*) remaining=${remaining#*,} ;; *) remaining= ;; esac
      case "$mirror" in
        https://*\ *|https://) : ;;
        https://*) sources="$sources ${mirror%/}/$release_base" ;;
      esac
    done ;;
esac

download() { # download <source base> <release path> <output> <timeout> <retries>
  curl -fsSL --retry "$5" --retry-all-errors --retry-delay 1 --retry-max-time 380 \
    --connect-timeout 5 --max-time "$4" "$1/$2" -o "$3"
}

select_source() { # select_source <release path> <timeout>: race every source once
  select_dir=$(mktemp -d) || return 1
  select_path=$1
  printf 'Selecting the fastest reachable release source:\n' >&2
  index=0
  pids=
  for base in $sources; do
    index=$((index+1))
    printf '  probing %s\n' "$base" >&2
    # Publish the result with an atomic rename so the polling loop below only
    # ever observes complete status files, never a truncated one.
    { if download "$base" "$select_path" "$select_dir/out.$index" "$2" 0; then state=done; else state=fail; fi
      printf '%s\n' "$state" > "$select_dir/next.$index"
      mv "$select_dir/next.$index" "$select_dir/st.$index"; } 2>/dev/null &
    pids="$pids $!"
  done
  total=$index
  winner=
  failed=0
  deadline=$(( $(date +%s) + 20 ))
  while :; do
    index=0
    for base in $sources; do
      index=$((index+1))
      if [ -f "$select_dir/st.$index" ]; then
        state=$(cat "$select_dir/st.$index")
        if [ "$state" = done ]; then winner=$index; break; fi
        mv "$select_dir/st.$index" "$select_dir/seen.$index"
        failed=$((failed+1))
      fi
    done
    if [ -n "$winner" ] || [ "$failed" -eq "$total" ] || [ "$(date +%s)" -ge "$deadline" ]; then break; fi
    sleep 0.2
  done
  for pid in $pids; do kill "$pid" 2>/dev/null || :; done
  wait 2>/dev/null || :
  if [ -z "$winner" ]; then rm -rf "$select_dir"; select_dir=; return 1; fi
  index=0
  for base in $sources; do
    index=$((index+1))
    if [ "$index" = "$winner" ]; then source_base=$base; fi
  done
  printf 'Using release source: %s\n' "$source_base" >&2
}

fetch_file() { # fetch_file <release path> <output> <timeout>: chosen source first, then the rest
  if download "$source_base" "$1" "$2" "$3" 2; then return 0; fi
  for base in $sources; do
    if [ "$base" != "$source_base" ]; then
      printf 'Source failed; trying next: %s\n' "$base" >&2
      if download "$base" "$1" "$2" "$3" 1; then return 0; fi
    fi
  done
  return 1
}
select_dir=
probed=no
if [ "$version" = latest ]; then
  select_source 'latest/download/VERSION' 15 || fail 'Cannot resolve latest release via GitHub or mirrors; set SIAGENT_GITHUB_MIRRORS to configure fallback prefixes.'
  version=$(cat "$select_dir/out.$winner")
  rm -rf "$select_dir"; select_dir=
  probed=yes
fi
printf '%s\n' "$version" | awk 'BEGIN {ok=1} NR != 1 || $0 !~ /^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$/ {ok=0} END {exit !ok}' || fail 'Version must be a release tag, such as v0.1.0.'

umask 022
mkdir -p "$prefix/versions" "$bin_dir"
prefix=$(CDPATH= cd -- "$prefix" && pwd -P)
bin_dir=$(CDPATH= cd -- "$bin_dir" && pwd -P)
# Refuse to replace an unrelated command or a non-installer directory.
if [ -e "$bin_dir/siagent" ] || [ -L "$bin_dir/siagent" ]; then
  [ -L "$bin_dir/siagent" ] && [ "$(readlink "$bin_dir/siagent")" = "$prefix/siagent" ] || fail "Command already exists: $bin_dir/siagent; choose --bin-dir."
fi
if [ ! -f "$prefix/.siagent-install" ] && { [ -e "$prefix/siagent" ] || [ -e "$prefix/current" ] || [ -L "$prefix/current" ]; }; then
  fail "Installation directory is already occupied: $prefix"
fi
if [ -e "$prefix/current" ] && [ ! -L "$prefix/current" ]; then fail "$prefix/current must be a symlink"; fi
mkdir "$prefix/.install-lock" 2>/dev/null || fail "Another install is running (lock: $prefix/.install-lock)."
work_dir=
cleanup() {
  if [ -n "$work_dir" ]; then rm -rf "$work_dir"; fi
  if [ -n "$select_dir" ]; then rm -rf "$select_dir"; fi
  rmdir "$prefix/.install-lock" 2>/dev/null || :
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
work_dir=$(mktemp -d "$prefix/.install.XXXXXX")
name=siagent-$platform-$architecture
asset=$name.tar.xz
printf 'Installing SiAgent %s (%s/%s)\n' "$version" "$platform" "$architecture"
# Pinned versions race on SHA256SUMS, so the probe result is the real file;
# latest already raced on VERSION and reuses the chosen source.
if [ "$probed" = yes ]; then
  fetch_file "download/$version/SHA256SUMS" "$work_dir/SHA256SUMS" 12 || fail 'Cannot download SHA256SUMS via GitHub or mirrors.'
else
  select_source "download/$version/SHA256SUMS" 15 || fail 'Cannot download SHA256SUMS via GitHub or mirrors; set SIAGENT_GITHUB_MIRRORS to configure fallback prefixes.'
  mv "$select_dir/out.$winner" "$work_dir/SHA256SUMS"
  rm -rf "$select_dir"; select_dir=
fi
# Report the archive transfer on one bounded line so the application updater can
# show a progress bar. The expected total is supplied by the updater, which
# already probed the release size; without it the bar stays indeterminate.
# Keeping this out of the download path avoids an extra request that would
# disturb source fallback and its tests.
case $(uname -s) in
  Linux) stat_size() { stat -c %s "$1" 2>/dev/null || printf 0; } ;;
  *) stat_size() { stat -f %z "$1" 2>/dev/null || printf 0; } ;;
esac
scale_total=${SIAGENT_RELEASE_TOTAL:-0}
case "$scale_total" in ''|*[!0-9]*) scale_total=0 ;; esac
( while :; do
    printf 'progress: %s %s\n' "$(stat_size "$work_dir/$asset")" "$scale_total"
    sleep 0.4
  done ) &
reporter=$!
fetch_file "download/$version/$asset" "$work_dir/$asset" 120 || fail 'Cannot download release via GitHub or mirrors.'
kill "$reporter" 2>/dev/null || :
wait "$reporter" 2>/dev/null || :
trap cleanup EXIT
expected=$(awk -v file="$asset" '$2 == file || $2 == "./" file {print $1}' "$work_dir/SHA256SUMS")
[ "${#expected}" -eq 64 ] || fail "Missing or ambiguous checksum for $asset"
case "$expected" in *[!0-9a-fA-F]*) fail 'Invalid SHA-256 checksum' ;; esac
if [ "$hash_tool" = sha256sum ]; then actual=$(sha256sum "$work_dir/$asset" | awk '{print $1}')
else actual=$(shasum -a 256 "$work_dir/$asset" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || fail 'SHA-256 mismatch; installation was not activated.'
tar -xf "$work_dir/$asset" -C "$work_dir" || fail 'Cannot extract release; ensure tar supports XZ.'
app=$work_dir/$name
[ -x "$app/siagent" ] && [ -x "$app/libexec/node" ] && [ -f "$app/runtime/dist/index.js" ] || fail 'Incomplete release package.'
"$app/libexec/node" -e 'require("node:sqlite")' || fail 'Bundled Node cannot run on this system.'
"$app/siagent" -help >/dev/null 2>&1 || fail 'SiAgent cannot run on this system.'
destination=$prefix/versions/$version-$platform-$architecture
if [ -e "$destination" ]; then
  [ -f "$destination/.archive-sha256" ] && [ "$(cat "$destination/.archive-sha256")" = "$expected" ] || fail "Existing version differs or is incomplete: $destination"
else
  printf '%s\n' "$expected" > "$app/.archive-sha256"
  mv "$app" "$destination"
fi
# The launcher resolves its own symlink, and keeps data outside version directories.
cat > "$work_dir/siagent" <<'LAUNCHER'
#!/bin/sh
set -eu
launcher=$0
case "$launcher" in */*) ;; *) launcher=$(command -v "$launcher") ;; esac
while [ -L "$launcher" ]; do
  target=$(readlink "$launcher")
  case "$target" in /*) launcher=$target ;; *) launcher=$(dirname "$launcher")/$target ;; esac
done
base=$(CDPATH= cd -- "$(dirname "$launcher")" && pwd -P)
exec "$base/current/siagent" -data-dir "${SIAGENT_DATA_DIR:-$base/data}" "$@"
LAUNCHER
chmod 755 "$work_dir/siagent"
mv -f "$work_dir/siagent" "$prefix/siagent"
: > "$prefix/.siagent-install"
printf '%s\n' "$repository" > "$prefix/.siagent-repository"
ln -sfn "$destination" "$prefix/current"
ln -sfn "$prefix/siagent" "$bin_dir/siagent"

if [ "$modify_path" = yes ]; then
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *)
      user_shell=${SHELL:-sh}
      case "${user_shell##*/}" in
        zsh) profile=$HOME/.zshrc ;;
        bash) if [ "$platform" = darwin ]; then profile=$HOME/.bash_profile; else profile=$HOME/.bashrc; fi ;;
        *) profile=$HOME/.profile ;;
      esac
      # Quote custom directories, including spaces and apostrophes, as shell literals.
      quoted_bin=$(printf '%s' "$bin_dir" | sed "s/'/'\\\\''/g")
      line="export PATH='$quoted_bin':\"\$PATH\""
      if ! grep -Fqx "$line" "$profile" 2>/dev/null; then printf '\n# SiAgent\n%s\n' "$line" >> "$profile"; fi
      printf 'PATH updated in %s; open a new terminal to use siagent.\n' "$profile"
      ;;
  esac
fi
printf 'Installed: %s\nData: %s/data\nStart: "%s/siagent"\nWeb: http://127.0.0.1:8080\n' "$destination" "$prefix" "$bin_dir"

if [ "$service" = yes ]; then
  printf '\nInstalling the per-user service (auto-start at login)...\n'
  if "$bin_dir/siagent" service install; then
    printf 'Service installed. Check it with: siagent service status\n'
    if [ "$platform" = darwin ]; then
      printf 'First-run token is in: tail -n 50 "$HOME/Library/Logs/SiAgent/siagent.log"\n'
    else
      printf 'First-run token is in: journalctl --user -u siagent -n 50\n'
    fi
  else
    printf 'Service was not installed; SiAgent itself is installed and can be started with: "%s/siagent"\n' "$bin_dir"
  fi
else
  printf 'Service installation skipped (--no-service). Start manually with: "%s/siagent"\n' "$bin_dir"
fi
