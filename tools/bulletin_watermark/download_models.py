import hashlib
from http.client import IncompleteRead
import sys
import time
from pathlib import Path
from urllib.request import urlopen


BASE_URL = "https://cai-watermark.adobe.net/watermarking/trustmark-models"
MODELS = {
    "trustmark_Q.yaml": "43f37103f92efa8bd6b1c5902bb537cc12a981dc699fca19d9bb7de8c62d03d9",
    "encoder_Q.ckpt": "dc382c3f6b4fd568b27d6fbb763d6ffc2d2587126d84afe9d5ee95b4c5d99826",
    "decoder_Q.ckpt": "e3d9cea5406a26590735719f8f15cb10802b11852ae69047eaf4cf17214df781",
}


def download(url, target, *, expected_sha256, attempts=5, timeout=60, retry_delay=2):
    target = Path(target)
    partial = target.with_suffix(f"{target.suffix}.part")
    target.parent.mkdir(parents=True, exist_ok=True)

    for attempt in range(1, attempts + 1):
        try:
            print(f"Downloading {target.name} (attempt {attempt}/{attempts})", flush=True)
            digest = hashlib.sha256()
            with urlopen(url, timeout=timeout) as response, partial.open("wb") as output:
                while chunk := response.read(1024 * 1024):
                    output.write(chunk)
                    digest.update(chunk)
            if digest.hexdigest() != expected_sha256:
                raise OSError(f"{target.name}: checksum mismatch (sha256={digest.hexdigest()})")
            partial.replace(target)
            return
        except (OSError, IncompleteRead) as error:
            partial.unlink(missing_ok=True)
            print(f"Download failed for {target.name}: {error}", file=sys.stderr, flush=True)
            if attempt == attempts:
                raise
            time.sleep(retry_delay)


def main():
    destination = Path(sys.argv[1])
    for name, expected_sha256 in MODELS.items():
        download(f"{BASE_URL}/{name}", destination / name, expected_sha256=expected_sha256)


if __name__ == "__main__":
    main()
