"""Receive an immutable release over the Russia SSH relay; never log credentials."""

import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    raw = sys.stdin.buffer.read(65537)
    if len(raw) > 65536:
        raise ValueError("release too large")
    release = json.loads(raw)
    if set(release) != {"revision", "image", "user", "token"}:
        raise ValueError("invalid release fields")
    revision, image = release["revision"], release["image"]
    if not re.fullmatch(r"[a-f0-9]{40}", revision) or not re.fullmatch(
        r"ghcr.io/mralexandrovorganization/stash-worker-cpu@sha256:[a-f0-9]{64}", image
    ):
        raise ValueError("invalid immutable release")
    lock = (Path.home() / ".stash-server-deploy.lock").open("a")
    fcntl.flock(lock, fcntl.LOCK_EX)
    root = Path(__file__).resolve().parents[1]
    os.chdir(root)
    if subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip() != str(root):
        raise ValueError("wrong checkout")
    if subprocess.check_output(["git", "status", "--porcelain"], text=True).strip():
        raise ValueError("dirty checkout")
    if (
        subprocess.check_output(["git", "branch", "--show-current"], text=True).strip()
        != "main"
    ):
        raise ValueError("wrong branch")
    run("git", "fetch", "--quiet", "deployment-source", "refs/heads/main")
    if (
        subprocess.check_output(["git", "rev-parse", "FETCH_HEAD"], text=True).strip()
        != revision
    ):
        raise ValueError("stale release")
    run("git", "merge", "--ff-only", "--quiet", revision)
    with tempfile.TemporaryDirectory(prefix="stash-registry-") as directory:
        env = {**os.environ, "SERVER_IMAGE": image, "DOCKER_CONFIG": directory}
        run(
            "docker",
            "login",
            "ghcr.io",
            "-u",
            release["user"],
            "--password-stdin",
            input=release["token"].encode(),
            env=env,
            stdout=subprocess.DEVNULL,
        )
        run("make", "deploy-image", env=env)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, subprocess.CalledProcessError):
        raise SystemExit(
            "Stash deployment failed; inspect checkout and runtime without printing release data"
        ) from None
