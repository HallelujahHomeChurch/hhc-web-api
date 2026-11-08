"""Synthetic fixture checks; generation runs locally, never in the API."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import xml.etree.ElementTree as ET

from probe import PAGE_COUNTS, ProbeError, probe, verify_xml

TOKENS = {"TEXT測", "EXCLUDED", "COVER_WELCOME", "COVER_WORSHIP", "COVER_WORK", "COVER_WORD",
          "BODY_START", "HYMN_LYRICS", "BACK_SUMMARY", "ANNOUNCEMENTS", "VICTORIES_PRAYERS"}
EXCLUDED_TOP = 464


def fixture_tokens(value):
    pattern = '|'.join(re.escape(token) for token in sorted(TOKENS, key=len, reverse=True))
    if re.sub(pattern, '', value).strip():
        raise ProbeError("fixture_text_not_allowlisted")
    return re.findall(pattern, value)


def verify_metadata(output):
    metadata = dict(line.split(b":", 1) for line in output.splitlines() if b":" in line)
    expected = {b"Title": b"HHC sanitized geometry fixture", b"Author": b"HHC synthetic fixture",
                b"Subject": b"unspecified", b"Keywords": b"", b"Creator": b"anonymous",
                b"Producer": b"ReportLab PDF Library - (opensource)", b"Custom Metadata": b"no",
                b"Metadata Stream": b"no", b"JavaScript": b"no", b"Form": b"none", b"Encrypted": b"no"}
    if any(metadata.get(key, b"missing").strip() != value for key, value in expected.items()):
        raise ProbeError("fixture_metadata_not_allowlisted")


def verify_pixels(data):
    if data not in {b"P6\n16 16\n255\n" + bytes(color) * 256
                    for color in ((128, 160, 192), (248, 248, 248))}:
        raise ProbeError("fixture_unknown_image")


def verify_pdf_features(data):
    # Restricted ReportLab fixture profile: object dictionaries are not in
    # compressed object streams. Reject those rather than trying to parse PDFs.
    forbidden = {b"Annot", b"Annots", b"AA", b"OpenAction", b"AcroForm", b"JavaScript", b"JS",
                 b"ObjStm", b"EmbeddedFiles", b"Filespec", b"URI", b"XFA", b"RichMedia", b"Launch"}
    for name in re.findall(rb"/([A-Za-z0-9#]+)", data):
        decoded = re.sub(rb"#([0-9a-fA-F]{2})", lambda match: bytes([int(match[1], 16)]), name)
        if decoded in forbidden:
            raise ProbeError("fixture_unsupported_pdf_feature")


def font_definitions(root):
    fonts = {font.get("id"): {"id": font.get("id"), "size": float(font.get("size")),
                              "family": font.get("family"), "color": font.get("color")}
             for font in root.iter("fontspec")}
    return sorted(fonts.values(), key=lambda font: font["id"])


def image_slots(root):
    return [{"page": int(page.get("number")), **{k: image.get(k) for k in ("top", "left", "width", "height")}}
            for page in root.findall("page") for image in page.findall("image")]


def reading_order(page, columns):
    midpoint = float(page.get("width")) / 2
    return sorted(page.findall("text"), key=lambda x: (
        int(float(x.get("left")) >= midpoint) if columns else 0,
        round(float(x.get("top")), 2), float(x.get("left")),
    ))


def fixture_geometry(xml, issue):
    root = ET.fromstring(xml)
    expected = {"COVER_WELCOME": 1, "COVER_WORSHIP": 1, "COVER_WORK": 1, "COVER_WORD": 1,
                "BODY_START": 2, "HYMN_LYRICS": PAGE_COUNTS[issue] - 1,
                "BACK_SUMMARY": PAGE_COUNTS[issue], "ANNOUNCEMENTS": PAGE_COUNTS[issue],
                "VICTORIES_PRAYERS": PAGE_COUNTS[issue]}
    seen, pages = {}, []
    for index, page in enumerate(root.findall("page"), 1):
        boxes = []
        for text in reading_order(page, index == PAGE_COUNTS[issue] - 1):
            value = ''.join(text.itertext()).strip()
            if not value:
                continue
            tokens = fixture_tokens(value)
            for token in tokens:
                if token in expected:
                    if token in seen or expected[token] != index:
                        raise ProbeError("fixture_component_page_mismatch")
                    seen[token] = index
            if index == PAGE_COUNTS[issue] and float(text.get("top")) >= EXCLUDED_TOP and tokens != ["EXCLUDED"]:
                raise ProbeError("fixture_excluded_region_has_text")
            boxes.append({"text": value, **{k: round(float(text.get(k)), 3) for k in ("top", "left", "width", "height")},
                          "font": text.get("font")})
        pages.append({"number": index, "width": page.get("width"), "height": page.get("height"), "boxes": boxes})
    if seen != expected:
        raise ProbeError("fixture_components_missing")
    lyric = pages[-2]["boxes"]
    if not any(b["left"] < 297 for b in lyric) or not any(b["left"] >= 297 for b in lyric):
        raise ProbeError("fixture_lyrics_missing_column")
    return {"components": seen, "fonts": font_definitions(root), "pages": pages}


def extract_xml(path, directory):
    output = Path(directory) / "geometry.xml"
    subprocess.run(["pdftohtml", "-xml", "-zoom", "1", "-noroundcoord", "-hidden", "-nomerge",
                    "-fontfullname", str(path.resolve()), str(output)], cwd=directory,
                   capture_output=True, check=True, timeout=120)
    return output.read_bytes()


def inspect_fixture(path, issue):
    report = probe(path, issue, "pdftohtml")
    fonts = subprocess.run(["pdffonts", str(path)], capture_output=True, check=True, timeout=30).stdout.decode()
    entries = [line.split() for line in fonts.splitlines()[2:] if line.strip()]
    if not entries or any(not re.fullmatch(r"[A-Z]{6}\+NotoSansTC-Thin", entry[0]) for entry in entries):
        raise ProbeError("fixture_unknown_font")
    info = subprocess.run(["pdfinfo", str(path)], capture_output=True, check=True, timeout=30).stdout
    verify_metadata(info)
    verify_pdf_features(path.read_bytes())
    images = subprocess.run(["pdfimages", "-list", str(path)], capture_output=True, check=True, timeout=30).stdout.decode()
    for line in images.splitlines()[2:]:
        fields = line.split()
        if fields and (fields[2:8] != ["image", "16", "16", "rgb", "3", "8"]):
            raise ProbeError("fixture_unknown_image")
    with tempfile.TemporaryDirectory(prefix="bulletin-fixture-") as directory:
        subprocess.run(["pdfimages", str(path.resolve()), str(Path(directory) / "pixels")],
                       capture_output=True, check=True, timeout=30)
        for image in Path(directory).glob("pixels-*"):
            verify_pixels(image.read_bytes())
        xml = extract_xml(path, directory)
        verify_xml(xml, issue)
        geometry = fixture_geometry(xml, issue)
        root = ET.fromstring(xml)
        geometry["images"] = [{**slot, "sha256": hashlib.sha256(
            (Path(directory) / image.get("src")).read_bytes()).hexdigest()}
            for slot, image in zip(image_slots(root), root.iter("image"), strict=True)]
    geometry["poppler_version"] = report["poppler_version"]
    return report, geometry


def verify_fixture(path, issue, golden):
    report, geometry = inspect_fixture(path, issue)
    if geometry != json.loads(golden.read_text()):
        raise ProbeError("fixture_geometry_or_image_mismatch")
    return report


def generate(source, issue, font_path, output, source_xml=None):
    # Only synthetic output leaves this local process; original text is transient.
    from PIL import Image
    from reportlab.lib.utils import ImageReader
    from reportlab.pdfbase import pdfmetrics
    from reportlab.pdfbase.ttfonts import TTFont
    from reportlab.pdfgen.canvas import Canvas

    pdfmetrics.registerFont(TTFont("Fixture", str(font_path)))
    output.mkdir(parents=True, exist_ok=True)
    target = output / f"{issue}-sanitized.pdf"
    with tempfile.TemporaryDirectory(prefix="bulletin-sanitize-") as directory:
        root = ET.fromstring(source_xml if source_xml is not None else extract_xml(source, directory))
        canvas = Canvas(str(target), pagesize=(595.32, 841.92), invariant=1, initialFontName="Fixture")
        canvas.setTitle("HHC sanitized geometry fixture")
        canvas.setAuthor("HHC synthetic fixture")
        fonts, markers = {}, set()
        for index, page in enumerate(root.findall("page"), 1):
            fonts.update({f.get("id"): f for f in page.findall("fontspec")})
            # Poppler can export an entire page's vector art as one background.
            # Render synthetic image slots beneath text, never over it.
            for image in page.findall("image"):
                x, y, width, height = (float(image.get(k)) for k in ("left", "top", "width", "height"))
                if index == PAGE_COUNTS[issue] and y + height > EXCLUDED_TOP:
                    continue
                color = (248, 248, 248) if width > 500 and height > 700 else (128, 160, 192)
                canvas.drawImage(ImageReader(Image.new("RGB", (16, 16), color)), x,
                                 841.92 - y - height, width, height)
            for text in page.findall("text"):
                value = ''.join(text.itertext()).strip()
                if not value:
                    continue
                x, y, width, height = (float(text.get(k)) for k in ("left", "top", "width", "height"))
                if index == PAGE_COUNTS[issue] and y + height > EXCLUDED_TOP:
                    continue
                token = "TEXT測"
                if index == 1:
                    for needle, candidate in (("Welcome", "COVER_WELCOME"), ("Worship", "COVER_WORSHIP"),
                                              ("Work", "COVER_WORK"), ("Word", "COVER_WORD")):
                        if needle in value and candidate not in markers:
                            token = candidate
                elif index == 2 and "BODY_START" not in markers:
                    token = "BODY_START"
                elif index == PAGE_COUNTS[issue] - 1 and "HYMN_LYRICS" not in markers:
                    token = "HYMN_LYRICS"
                elif index == PAGE_COUNTS[issue]:
                    if "BACK_SUMMARY" not in markers:
                        token = "BACK_SUMMARY"
                    elif "公佈欄" in value and "ANNOUNCEMENTS" not in markers:
                        token = "ANNOUNCEMENTS"
                    elif "得勝" in value and "VICTORIES_PRAYERS" not in markers:
                        token = "VICTORIES_PRAYERS"
                markers.add(token)
                size = float(fonts[text.get("font")].get("size"))
                if token == "TEXT測":
                    token *= max(1, round(width / pdfmetrics.stringWidth(token, "Fixture", size)))
                canvas.saveState()
                canvas.translate(x, 841.92 - y - height)
                canvas.scale(width / pdfmetrics.stringWidth(token, "Fixture", size), height / size)
                canvas.setFont("Fixture", size)
                canvas.drawString(0, size * .12, token)
                canvas.restoreState()
            if index == PAGE_COUNTS[issue]:
                canvas.setFillColorRGB(.92, .92, .92)
                canvas.rect(0, 0, 595.32, 841.92 - EXCLUDED_TOP, stroke=0, fill=1)
                canvas.setFillColorRGB(0, 0, 0)
                canvas.setFont("Fixture", 10)
                canvas.drawString(20, 20, "EXCLUDED")
            canvas.showPage()
        canvas.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--record-goldens", action="store_true", help="Record only after privacy scans; review the resulting diff")
    args = parser.parse_args()
    try:
        for issue in (1739, 1740):
            path = args.directory / f"{issue}-sanitized.pdf"
            golden = args.directory / f"{issue}-geometry.golden.json"
            if args.record_goldens:
                report, geometry = inspect_fixture(path, issue)
                golden.write_text(json.dumps(geometry, ensure_ascii=False, indent=2) + "\n")
            else:
                report = verify_fixture(path, issue, golden)
            print(json.dumps({"issue": issue, "fixture_pass": True, "poppler_version": report["poppler_version"]}))
    except (ProbeError, OSError, subprocess.SubprocessError, ValueError, TypeError, IndexError):
        print(json.dumps({"fixture_pass": False, "error_code": "fixture_validation_failed"}))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
