#!/usr/bin/env python3
"""Use the immutable image config digest across Docker storage implementations."""
import hashlib
import json
import re
from pathlib import PurePosixPath
import subprocess
import sys
import tarfile
import tempfile


def archive_config_digest(path, image):
    with tarfile.open(path, "r:*") as archive:
        members = archive.getmembers()
        names = [member.name for member in members]
        if len(set(names)) != len(names) or any(
            name.startswith("/") or ".." in PurePosixPath(name).parts for name in names
        ) or any(not (member.isfile() or member.isdir()) for member in members):
            raise ValueError("unsafe image archive")

        def read(name):
            member = archive.getmember(name)
            if not member.isfile() or member.size > 1024 * 1024:
                raise ValueError("invalid image metadata")
            return archive.extractfile(member).read()

        manifest = json.loads(read("manifest.json"))
        if len(manifest) != 1 or not (manifest[0]["RepoTags"] == [image] or
            (manifest[0]["RepoTags"] in (None, []) and re.fullmatch(r".+@sha256:[0-9a-f]{64}", image))):
            raise ValueError("unexpected image archive tags")
        config = read(manifest[0]["Config"])
        metadata = json.loads(config)
        if metadata.get("os") != "linux" or metadata.get("architecture") != "amd64":
            raise ValueError("image platform must be linux/amd64")
        if any(not archive.getmember(layer).isfile() for layer in manifest[0]["Layers"]):
            raise ValueError("missing image layer")
        return "sha256:" + hashlib.sha256(config).hexdigest()


def image_config_digest(image):
    with tempfile.TemporaryDirectory() as directory:
        path = directory + "/image.tar"
        subprocess.run(["docker", "image", "save", "--output", path, image], check=True)
        return archive_config_digest(path, image)


if __name__ == "__main__":
    if len(sys.argv) == 2:
        print(image_config_digest(sys.argv[1]))
    elif len(sys.argv) == 3:
        print(archive_config_digest(sys.argv[1], sys.argv[2]))
    else:
        sys.exit("usage: image_config_digest.py IMAGE | ARCHIVE IMAGE")
