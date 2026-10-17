#!/usr/bin/env python3
"""Resolve and reserve one release tag before the native build matrix starts."""
import os
import re
import subprocess

VERSION = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?")


def next_version(tags):
    versions = [tuple(map(int, match.groups())) for tag in tags
                if (match := VERSION.fullmatch(tag))]
    major, minor, patch = max(versions, default=(0, 0, 0))
    return f"v{major}.{minor}.{patch + 1}"


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def resolve_release(requested, event, ref_name):
    git("fetch", "origin", "--tags", "--force")
    tags = git("tag", "--list").splitlines()
    tag = ref_name if event == "push" else requested.strip() or next_version(tags)
    if not VERSION.fullmatch(tag):
        raise ValueError("Version must be vMAJOR.MINOR.PATCH with an optional prerelease suffix")
    if tag in tags:
        commit = git("rev-parse", f"refs/tags/{tag}^{{commit}}")
    else:
        if event == "push":
            raise ValueError("Pushed version tag was not found")
        commit = git("rev-parse", "HEAD")
        git("tag", tag, commit)
        # No force: a concurrent external tag push must fail instead of being overwritten.
        git("push", "origin", f"refs/tags/{tag}")
    return tag, commit


if __name__ == "__main__":
    tag, commit = resolve_release(os.environ.get("REQUESTED_TAG", ""),
                                  os.environ["GITHUB_EVENT_NAME"],
                                  os.environ["GITHUB_REF_NAME"])
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"tag={tag}\ncommit={commit}\n")
    print(f"Release {tag} at {commit}")
