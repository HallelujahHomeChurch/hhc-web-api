"""Controlled geometry preflight. Never emits source text, XML, or paths."""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import tempfile
import xml.etree.ElementTree as ET


class ProbeError(Exception):
    pass


PAGE_COUNTS = {1731: 20, 1733: 18, 1739: 12, 1740: 16}


def expected_box(issue, page):
    if issue == 1731:
        return (595.32, 841.92) if page == 20 else (612, 792)
    if issue in (1739, 1740):
        return (595.32, 841.92)
    return None  # Historical 1733 page-box baseline still needs its original.


def verify_pdfinfo(output, issue):
    boxes = re.findall(rb"Page\s+(\d+) size:\s+([\d.]+) x ([\d.]+) pts", output)
    if len(boxes) != PAGE_COUNTS[issue]:
        raise ProbeError("page_count_mismatch")
    for index, (number, width, height) in enumerate(boxes, 1):
        if int(number) != index:
            raise ProbeError("page_sequence_mismatch")
        dimensions = (positive(width), positive(height))
        expected = expected_box(issue, index)
        if expected and any(abs(actual - target) > 0.02 for actual, target in zip(dimensions, expected)):
            raise ProbeError("page_box_mismatch")


def positive(value):
    try:
        number = float(value)
    except (TypeError, ValueError):
        raise ProbeError("invalid_geometry") from None
    if not math.isfinite(number) or number <= 0:
        raise ProbeError("invalid_geometry")
    return number


def verify_xml(xml, issue):
    try:
        root = ET.fromstring(xml.decode("utf-8", errors="strict"))
    except (UnicodeError, ET.ParseError):
        raise ProbeError("invalid_xml_encoding") from None
    pages = root.findall("page")
    if root.tag != "pdf2xml" or len(pages) != PAGE_COUNTS[issue]:
        raise ProbeError("page_count_mismatch")
    cjk_count = 0
    fonts = {}
    for index, page in enumerate(pages, 1):
        if page.get("number") != str(index):
            raise ProbeError("page_sequence_mismatch")
        width, height = positive(page.get("width")), positive(page.get("height"))
        expected = expected_box(issue, index)
        if expected:
            # Poppler truncates XML page dimensions even with -noroundcoord.
            # pdfinfo separately checks the precise source page boxes.
            if any(abs(actual - target) >= 1 for actual, target in zip((width, height), expected)):
                raise ProbeError("page_box_mismatch")
        fonts.update({font.get("id"): font for font in page.findall("fontspec")})
        text_count = 0
        for text in page.findall("text"):
            value = "".join(text.itertext()).strip()
            if not value:
                continue
            if "\ufffd" in value:
                raise ProbeError("undecodable_text")
            positive(text.get("width"))
            positive(text.get("height"))
            for coordinate in ("top", "left"):
                try:
                    valid = math.isfinite(float(text.get(coordinate)))
                except (TypeError, ValueError):
                    valid = False
                if not valid:
                    raise ProbeError("invalid_geometry")
            font = fonts.get(text.get("font"))
            if font is None or not font.get("family"):
                raise ProbeError("missing_font")
            positive(font.get("size"))
            text_count += 1
            cjk_count += bool(re.search(r"[\u3400-\u9fff]", value))
        if not text_count:
            raise ProbeError("empty_page")
    if not cjk_count:
        raise ProbeError("missing_cjk_text")
    return {"page_count": True, "page_boxes": True, "text_geometry": True, "decoded_cjk": True}


def probe(path, issue, executable):
    try:
        with path.open("rb") as source:
            checksum = hashlib.file_digest(source, "sha256").hexdigest()
        version = subprocess.run([executable, "-v"], capture_output=True, timeout=10, check=True)
        match = re.search(rb"pdftohtml version ([0-9.]+)", version.stdout + version.stderr)
        if not match:
            raise ProbeError("unknown_poppler_version")
        pdfinfo = Path(executable).with_name("pdfinfo") if "/" in executable else Path("pdfinfo")
        info = subprocess.run([str(pdfinfo), "-f", "1", "-l", str(PAGE_COUNTS[issue]), "-box", str(path.resolve())],
                              capture_output=True, timeout=30, check=True, env={**os.environ, "LC_ALL": "C"})
        verify_pdfinfo(info.stdout, issue)
        with tempfile.TemporaryDirectory(prefix="bulletin-geometry-") as directory:
            output = Path(directory) / "geometry.xml"
            subprocess.run(
                [executable, "-xml", "-zoom", "1", "-noroundcoord", "-hidden", "-nomerge",
                 "-fontfullname", str(path.resolve()), str(output)],
                cwd=directory, capture_output=True, timeout=120, check=True,
            )
            checks = verify_xml(output.read_bytes(), issue)
        return {"source_sha256": checksum, "poppler_version": match[1].decode(),
                "checks": checks, "geometry_pass": True}
    except ProbeError:
        raise
    except FileNotFoundError:
        raise ProbeError("missing_input_or_poppler") from None
    except (OSError, subprocess.SubprocessError):
        raise ProbeError("poppler_failed") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("issue", type=int, choices=tuple(PAGE_COUNTS))
    parser.add_argument("pdf", type=Path)
    args = parser.parse_args()
    try:
        result = probe(args.pdf, args.issue, os.environ.get("PDFTOHTML", "pdftohtml"))
    except ProbeError as error:
        print(json.dumps({"geometry_pass": False, "error_code": str(error)}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
