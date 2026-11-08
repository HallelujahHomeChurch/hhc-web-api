#!/usr/bin/env python3
"""Run every Go package once, using two isolated databases on one runner."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from urllib.parse import parse_qs, unquote, urlparse

MODULE = "github.com/HallelujahHomeChurch/hhc-web-api"
STORAGE = frozenset({MODULE + "/internal/migrations", MODULE + "/internal/postgres"})


def partition(packages):
    if len(set(packages)) != len(packages) or not (STORAGE | {MODULE}).issubset(packages):
        raise ValueError("duplicate packages or missing expected package")
    # New packages remain in the core lane; migrations/postgres stay serial.
    return [[p for p in packages if p not in STORAGE],
            [p for p in packages if p in STORAGE]]


def database_environments(environ):
    if environ.get("HHC_WEB_ALLOW_DESTRUCTIVE_TEST") != "1":
        raise ValueError("disposable database authorization required")
    environments, identities = [], []
    for key in ("HHW_TEST_DATABASE_URL", "HHW_TEST_STORAGE_DATABASE_URL"):
        value = environ.get(key, "")
        uri = urlparse(value)
        name = unquote(uri.path.removeprefix("/"))
        if (uri.scheme not in ("postgres", "postgresql") or
                uri.hostname not in ("localhost", "127.0.0.1") or
                not name.startswith("hhc_web_") or not name.endswith("_test") or
                set(parse_qs(uri.query)) - {"sslmode"}):
            raise ValueError("two explicit loopback test databases required")
        identities.append((uri.port or 5432, name))
        child = {**environ, "HHW_TEST_DATABASE_URL": value, "TEST_DATABASE_URL": value}
        child.pop("TEST_POSTGRES_DSN", None)  # Bootstrap owns the admin DSN before tests start.
        environments.append(child)
    if identities[0][0] != identities[1][0] or identities[0][1] == identities[1][1]:
        raise ValueError("lanes require different databases on the same disposable server")
    if identities[1][1] != "hhc_web_test":
        raise ValueError("storage retirement fixtures require hhc_web_test")
    return environments


def verify_events(path, packages):
    terminal = []
    with path.open() as source:
        for line in source:
            event = json.loads(line)
            if event.get("Action") == "fail":
                raise ValueError("failed Go test event")
            if not event.get("Test") and event.get("Action") in ("pass", "skip"):
                terminal.append(event["Package"])
    if sorted(terminal) != sorted(packages):
        raise ValueError("incomplete or duplicate package results")


def run(output):
    # Never leave a previous successful manifest usable after a failed run.
    output.unlink(missing_ok=True)
    environments = database_environments(os.environ)
    packages = subprocess.check_output(["go", "list", "./..."], text=True).splitlines()
    lanes = partition(packages)
    print(f"Go test lanes: {len(lanes[0])} core/contracts + {len(lanes[1])} storage packages",
          file=sys.stderr, flush=True)
    output.parent.mkdir(parents=True, exist_ok=True)
    processes = []
    with tempfile.TemporaryDirectory(prefix="go-lanes-", dir=output.parent) as directory:
        paths = [Path(directory) / f"{i}.jsonl" for i in range(len(lanes))]
        files = [path.open("w") for path in paths]
        try:
            for lane, stream, env in zip(lanes, files, environments):
                processes.append(subprocess.Popen(
                    ["go", "test", "-race", "-json", *lane, "-count=1", "-p=1"],
                    stdout=stream, env=env, start_new_session=True))
            while True:
                statuses = [p.poll() for p in processes]
                if any(code not in (None, 0) for code in statuses):
                    raise RuntimeError("Go test lane failed")
                if all(code == 0 for code in statuses):
                    break
                time.sleep(0.05)
            for stream in files:
                stream.close()
            for path, lane in zip(paths, lanes):
                verify_events(path, lane)
            # Separate files prevent concurrent writers corrupting JSON records.
            merged = Path(directory) / "merged.jsonl"
            with merged.open("w") as destination:
                for path in paths:
                    destination.write(path.read_text())
            merged.replace(output)
        finally:
            for process in processes:
                if process.poll() is None:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                process.wait()
            for stream in files:
                stream.close()
            for path in paths:
                sys.stdout.write(path.read_text())


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    run(Path(".artifacts/go-tests/test-events.jsonl"))
