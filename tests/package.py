#!/usr/bin/env python3
"""Check a trusted local release with no system Node or tools on PATH."""
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request


def check_service(executable, arguments, working_dir, env, data_dir, log_path):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    url = f"http://127.0.0.1:{port}"
    http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with log_path.open("w+") as log:
        process = subprocess.Popen([str(executable), "-listen", f"127.0.0.1:{port}", *arguments],
                                   cwd=working_dir, env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise AssertionError("Packaged service exited before readiness")
                try:
                    with http.open(url + "/health/ready", timeout=1) as response:
                        if response.status == 200:
                            break
                except (urllib.error.URLError, TimeoutError):
                    pass
                time.sleep(0.1)
            else:
                raise AssertionError("Packaged Runtime did not become ready")
            with http.open(url + "/", timeout=2) as response:
                assert response.status == 200
                assert b'<div id="root">' in response.read()
            assert (data_dir / "platform.sqlite").exists()
            assert not (working_dir / "data").exists()
        except BaseException:
            log.flush()
            log.seek(0)
            print(log.read(), file=sys.stderr)
            raise
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        assert process.returncode == 0, process.returncode


def main():
    archive = Path(sys.argv[1]).resolve()
    with tempfile.TemporaryDirectory(prefix="siagent package ") as temporary:
        root = Path(temporary)
        with tarfile.open(archive) as bundle:
            bundle.extractall(root, filter="data")
        release = next(path for path in root.iterdir() if path.is_dir())
        executable = release / "siagent"
        node = release / "libexec/node"
        assert (release / "runtime/node_modules").is_dir()
        assert not (release / "runtime/node_modules/typescript").exists()
        assert not any((release / "runtime/node_modules").rglob("*.d.ts"))
        assert not any((release / "runtime/node_modules").rglob("*.js.map"))
        assert (release / "licenses/node-LICENSE").is_file()
        assert not (release / "start.sh").exists()
        unrelated = root / "unrelated working directory"
        unrelated.mkdir()
        env = dict(os.environ)
        for key in list(env):
            if key.startswith("SIAGENT_") or key in ("NODE_OPTIONS", "NODE_PATH"):
                env.pop(key)
        # The application's runtime and built-in channel SDK must work without PATH tools.
        tools = root / "empty-path"
        tools.mkdir()
        env["PATH"] = str(tools)
        version = subprocess.check_output([str(node), "--version"], env=env, text=True).strip()
        assert version == "v" + (release / "NODE_VERSION").read_text().strip()
        subprocess.run([str(node), "--input-type=module", "-e",
                        'await import("@wecom/aibot-node-sdk"); await import("node:sqlite")'],
                       cwd=release / "runtime", env=env, check=True, timeout=15)

        # Broken private runtime fails early, without silently using a host installation.
        saved_node = node.with_name("node.saved")
        node.rename(saved_node)
        try:
            missing = subprocess.run([str(executable)], cwd=unrelated, env=env,
                                     capture_output=True, text=True, timeout=15)
            assert missing.returncode == 1
            assert "cannot use Node.js runtime" in missing.stderr
            assert not (release / "data").exists()
        finally:
            saved_node.rename(node)

        # Resolve the release through a symlink, from a directory unrelated to the source.
        link = root / "agent-link"
        link.symlink_to(executable)
        check_service(link, [], unrelated, env, release / "data", root / "default.log")

        # Environment overrides work, and explicit flags take priority over the environment.
        external_data = root / "external-data"
        overridden_env = {**env, "SIAGENT_NODE": str(node), "SIAGENT_DATA_DIR": str(external_data)}
        check_service(executable, [], unrelated, overridden_env, external_data, root / "env.log")
        flag_data = root / "flag-data"
        overridden_env["SIAGENT_NODE"] = str(root / "missing-node")
        check_service(executable, ["-node", str(node), "-data-dir", str(flag_data),
                                  "-runtime", str(release / "runtime/dist/index.js")],
                      unrelated, overridden_env, flag_data, root / "flags.log")
    print("Package passed: private Node, channel SDK, empty PATH, symlink relocation, overrides, readiness, embedded Web and clean shutdown")


if __name__ == "__main__":
    main()
