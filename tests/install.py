#!/usr/bin/env python3
"""Exercise the installer against local releases, without touching the real HOME."""
import hashlib
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import io
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
from threading import Thread


INSTALLER = Path(__file__).resolve().parent.parent / "scripts/install.sh"


def write_release(root, version, target, bad_checksum=False):
    folder = root / "releases/download" / version
    folder.mkdir(parents=True)
    name = f"siagent-{target}"
    archive = folder / f"{name}.tar.xz"
    with tarfile.open(archive, "w:xz") as bundle:
        for filename, content, mode in (
            ("siagent", f'#!/bin/sh\nprintf "%s\\n" "{version}" "$@"\n', 0o755),
            ("libexec/node", "#!/bin/sh\nexit 0\n", 0o755),
            ("runtime/dist/index.js", "// fixture\n", 0o644),
            ("NODE_VERSION", "24.22.0\n", 0o644),
        ):
            member = tarfile.TarInfo(f"{name}/{filename}")
            data = content.encode()
            member.size, member.mode = len(data), mode
            bundle.addfile(member, io.BytesIO(data))
    checksum = "0" * 64 if bad_checksum else hashlib.sha256(archive.read_bytes()).hexdigest()
    (folder / "SHA256SUMS").write_text(f"{checksum}  ./{archive.name}\n")


def main():
    system = {"Linux": "linux", "Darwin": "darwin"}[platform.system()]
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
    target = f"{system}-{arch}"
    with tempfile.TemporaryDirectory(prefix="siagent install '") as temporary:
        root = Path(temporary).resolve()
        web = root / "web"
        write_release(web, "v1.0.0", target)
        write_release(web, "v1.1.0", target)
        write_release(web, "v1.2.0", target, bad_checksum=True)
        latest = web / "releases/tag/v1.1.0"
        latest.parent.mkdir(parents=True)
        latest.write_text("latest release")
        metadata = web / "releases/latest/download/VERSION"
        metadata.parent.mkdir(parents=True)
        metadata.write_text("v1.1.0\n")

        class Handler(SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=str(web), **kwargs)

            def do_GET(self):
                if self.path == "/releases/latest":
                    self.send_response(302)
                    self.send_header("Location", "/releases/tag/v1.1.0")
                    self.end_headers()
                else:
                    super().do_GET()

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            home = root / "home with ' apostrophe"
            home.mkdir()
            prefix, bin_dir = home / "app files", home / "bin files"
            env = dict(os.environ, HOME=str(home), SHELL="/bin/bash",
                       SIAGENT_RELEASE_BASE_URL=f"http://127.0.0.1:{server.server_port}/releases")
            for key in ("SIAGENT_DATA_DIR", "XDG_DATA_HOME", "SIAGENT_NODE"):
                env.pop(key, None)

            def install(*arguments, success=True):
                result = subprocess.run(["sh", str(INSTALLER), "--prefix", str(prefix),
                                         "--bin-dir", str(bin_dir), *arguments],
                                        env=env, capture_output=True, text=True, timeout=20)
                assert (result.returncode == 0) == success, result.stdout + result.stderr
                return result

            install("--version", "v1.0.0")
            launcher = bin_dir / "siagent"
            output = subprocess.check_output([str(launcher), "-listen", "127.0.0.1:8888"], env=env, text=True).splitlines()
            assert output == ["v1.0.0", "-data-dir", str(prefix / "data"), "-listen", "127.0.0.1:8888"]
            (prefix / "data").mkdir()
            (prefix / "data/keep.txt").write_text("persistent user data")
            first = (prefix / "current").resolve()
            install("--version", "v1.0.0")
            assert (prefix / "current").resolve() == first
            assert len(list((prefix / "versions").iterdir())) == 1

            # latest follows a redirect to a concrete tag; upgrades retain old code and data.
            install()
            current = (prefix / "current").resolve()
            assert current != first and first.is_dir()
            assert (prefix / "data/keep.txt").read_text() == "persistent user data"
            assert subprocess.check_output([str(launcher)], env=env, text=True).startswith("v1.1.0\n")
            override_env = {**env, "SIAGENT_DATA_DIR": str(root / "other-data")}
            assert str(root / "other-data") in subprocess.check_output([str(launcher)], env=override_env, text=True)

            profile = home / (".bash_profile" if system == "darwin" else ".bashrc")
            assert profile.read_text().count("# SiAgent") == 1
            resolved = subprocess.check_output(["sh", "-c", '. "$1"; command -v siagent', "sh", str(profile)],
                                               env=env, text=True).strip()
            assert resolved == str(launcher)

            failure = install("--version", "v1.2.0", success=False)
            assert "SHA-256 mismatch" in failure.stderr
            assert (prefix / "current").resolve() == current
            assert (prefix / "data/keep.txt").exists()
            assert not (prefix / ".install-lock").exists()
            assert not list(prefix.glob(".install.*"))
            install("--version", "v1.0.0\n../../escape", success=False)
            conflict = home / "conflict"
            conflict.mkdir()
            (conflict / "siagent").write_text("unrelated command")
            install("--version", "v1.0.0", "--bin-dir", str(conflict), success=False)
            assert (conflict / "siagent").read_text() == "unrelated command"
            # Windows feeds the LF script to `sh -s --` over stdin.
            before_profile = profile.read_text()
            piped = subprocess.run(["sh", "-s", "--", "--version", "v1.0.0", "--no-modify-path"],
                                   input=INSTALLER.read_text(), env=env, capture_output=True, text=True, timeout=20)
            assert piped.returncode == 0, piped.stdout + piped.stderr
            assert (home / ".local/bin/siagent").is_symlink()
            assert profile.read_text() == before_profile
            # Exercise GitHub-first fallback without accessing any public host.
            wrappers = root / "network wrappers"
            wrappers.mkdir()
            real_curl = shutil.which("curl")
            curl_wrapper = wrappers / "curl"
            curl_wrapper.write_text("#!/usr/bin/env python3\n" +
                "import os, subprocess, sys\n" +
                "args=sys.argv[1:]\n" +
                "for i,arg in enumerate(args):\n" +
                " if arg.startswith('https://github.com/'):\n" +
                "  open(os.environ['SIAGENT_CURL_LOG'],'a').write('official\\n');sys.exit(22)\n" +
                " if arg.startswith('https://mirror.test/https://github.com/vst93/SiAgent/releases'):\n" +
                "  open(os.environ['SIAGENT_CURL_LOG'],'a').write('mirror\\n')\n" +
                "  args[i]=os.environ['SIAGENT_FIXTURE_RELEASES']+arg.split('/SiAgent/releases',1)[1]\n" +
                "sys.exit(subprocess.call([os.environ['SIAGENT_REAL_CURL'],*args]))\n")
            curl_wrapper.chmod(0o755)
            mirror_env = {**env, "PATH": str(wrappers) + os.pathsep + env["PATH"],
                          "SIAGENT_RELEASE_BASE_URL": "https://github.com/vst93/SiAgent/releases",
                          "SIAGENT_GITHUB_MIRRORS": "https://mirror.test/",
                          "SIAGENT_FIXTURE_RELEASES": env["SIAGENT_RELEASE_BASE_URL"],
                          "SIAGENT_REAL_CURL": real_curl,
                          "SIAGENT_CURL_LOG": str(root / "curl.log")}
            mirror_result = subprocess.run(["sh", str(INSTALLER), "--prefix", str(root / "mirror-app"),
                                            "--bin-dir", str(root / "mirror-bin"), "--no-modify-path"],
                                           env=mirror_env, capture_output=True, text=True, timeout=20)
            assert mirror_result.returncode == 0, mirror_result.stdout + mirror_result.stderr
            attempts = (root / "curl.log").read_text().splitlines()
            assert attempts == ["official", "mirror"] * 3, attempts
            if len(sys.argv) > 1:
                from package import check_service
                actual = Path(sys.argv[1]).resolve()
                actual_release = web / "releases/download/v9.9.9"
                actual_release.mkdir()
                asset = actual_release / f"siagent-{target}.tar.xz"
                shutil.copyfile(actual, asset)
                digest = hashlib.sha256(asset.read_bytes()).hexdigest()
                (actual_release / "SHA256SUMS").write_text(f"{digest}  ./{asset.name}\n")
                install("--version", "v9.9.9")
                working = root / "unrelated working directory"
                working.mkdir()
                check_service(launcher, [], working, env, prefix / "data", root / "installed-service.log")
                assert (prefix / "data/keep.txt").read_text() == "persistent user data"
                print("Real release passed: installed command, private Node, readiness, Web and persistent data")
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
    print("Installer passed: version/platform selection, latest redirect, SHA-256, upgrades, persistent data, PATH quoting, idempotency and failed-install preservation")


if __name__ == "__main__":
    main()
