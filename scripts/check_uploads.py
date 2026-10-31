"""Run upload/GC integration tests on a disposable pinned PostgreSQL.

Uses synthetic credentials, a random loopback port, no host mounts, and never
reads application database configuration. The container and its volume are
removed on every exit, including test failures.
"""

import json
import os
import subprocess
import time
import uuid


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=120).stdout


def check():
    name = "stash-upload-check-" + uuid.uuid4().hex
    image = "pgvector/pgvector:" + os.environ["PGVECTOR_VERSION"]
    try:
        run(
            "docker", "run", "--detach", "--rm", "--name", name,
            "--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD=fixture",
            image,
        )
        for _ in range(120):
            logs = run("docker", "logs", name)
            ready = subprocess.run(
                ["docker", "exec", name, "pg_isready", "-U", "postgres"],
                capture_output=True, timeout=10,
            )
            if "PostgreSQL init process complete; ready for start up." in logs and ready.returncode == 0:
                break
            time.sleep(0.5)
        else:
            raise RuntimeError("fixture PostgreSQL did not become ready")
        ports = json.loads(run("docker", "inspect", name))[0]["NetworkSettings"]["Ports"]["5432/tcp"]
        port = next(p["HostPort"] for p in ports if p["HostIp"] == "127.0.0.1")
        env = os.environ.copy()
        env["STASH_UPLOAD_TEST_DATABASE_URL"] = f"postgres://postgres:fixture@127.0.0.1:{port}/postgres?sslmode=disable"
        subprocess.run(
            ["go", "test", "-race", "-count=1", "-timeout=180s", "-tags=integration", "./internal/service"],
            check=True, env=env, timeout=240,
        )
    finally:
        subprocess.run(
            ["docker", "rm", "--force", "--volumes", name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
        )


if __name__ == "__main__":
    check()
