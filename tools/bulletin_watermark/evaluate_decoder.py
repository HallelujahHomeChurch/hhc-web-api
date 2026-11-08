#!/usr/bin/env python3
"""Reproducible offline gate for exact retained-payload decoding."""
import argparse
import io
import json
from pathlib import Path
import random
import re
import resource
import subprocess
import tempfile
import time

import engine


TRANSFORMS = {'png', 'jpeg', 'scale', 'crop', 'rotation', 'screenshot', 'line', 'pdf_raster',
              'unmarked', 'unrelated', 'damaged', 'unknown_payload', 'wrong_issue', 'copied_metadata',
              'first_only', 'multi_receipt'}


def classify(expected_receipt_id, payload, retained):
    receipt_id = retained.get(payload)
    if receipt_id is None:
        return {'outcome': 'inconclusive', 'falseAttribution': False}
    return {'outcome': 'matched', 'receiptId': receipt_id,
            'falseAttribution': receipt_id != expected_receipt_id}


def summarize(rows, minimum_quality, metrics):
    false_attributions = sum(bool(row['falseAttribution']) for row in rows)
    correct_matches = sum(row['outcome'] == 'matched' and not row['falseAttribution'] for row in rows)
    accepted_below_threshold = sum(row['outcome'] == 'matched' and row.get('quality', 0) < minimum_quality for row in rows)
    return {
        'status': 'evaluated',
        'evaluationIdentity': engine.DECODER_VERSION,
        'modelSha256': engine.MODEL_SHA256,
        'minimumQuality': minimum_quality,
        'sampleSize': len(rows),
        'transformCoverage': sorted({row['transform'] for row in rows}),
        'correctMatches': correct_matches,
        'inconclusive': sum(row['outcome'] == 'inconclusive' for row in rows),
        'falseAttributions': false_attributions,
        'eligibleForDisclosure': bool(rows) and correct_matches > 0 and false_attributions == 0 and accepted_below_threshold == 0,
        'metrics': metrics,
        'cases': rows,
    }


def markdown(summary):
    transforms = ', '.join(summary['transformCoverage'])
    metrics = summary.get('metrics', {})
    return f"""# Bulletin Watermark Direct Decoder Evaluation

**Evaluation identity:** `{summary['evaluationIdentity']}`

**Model SHA-256:** `{summary['modelSha256']}`

**Minimum quality:** `{summary['minimumQuality']}`

**Sample size:** {summary['sampleSize']}

**Transform coverage:** {transforms}

**Correct matches:** {summary['correctMatches']}

**Inconclusive:** {summary['inconclusive']}

**False attributions:** {summary['falseAttributions']}

**Eligible for identity disclosure:** `{str(summary['eligibleForDisclosure']).lower()}`

**Cold model load:** {metrics.get('coldLoadSeconds', 0):.3f} seconds

**Warm mean decode:** {metrics.get('warmMeanSeconds', 0):.3f} seconds

**Batch decode:** {metrics.get('batchSeconds', 0):.3f} seconds

**Peak RSS:** {metrics.get('peakRssBytes', 0)} bytes

This gate reports only the frozen evaluated set. It is not a general zero-error claim; inconclusive results remain allowed.
"""


def load_manifest(path):
    data = engine.load_json(path)
    if not isinstance(data, dict) or set(data) != {'version', 'minimumQuality', 'retainedPayloads', 'cases'}:
        raise engine.JobError('invalid_decoder_evaluation')
    threshold = data['minimumQuality']
    retained = data['retainedPayloads']
    cases = data['cases']
    if data['version'] != 1 or type(threshold) not in (int, float) or not .5 <= threshold <= 1:
        raise engine.JobError('invalid_decoder_evaluation')
    if not isinstance(retained, dict) or not retained or not isinstance(cases, list) or not 1 <= len(cases) <= 100:
        raise engine.JobError('invalid_decoder_evaluation')
    if any(not isinstance(payload, str) or not re.fullmatch('[01]{61}', payload) or
           not isinstance(receipt, str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', receipt)
           for payload, receipt in retained.items()):
        raise engine.JobError('invalid_decoder_evaluation')
    ids = set()
    for case in cases:
        if not isinstance(case, dict) or set(case) != {'id', 'transform', 'payload', 'expectedReceiptId'}:
            raise engine.JobError('invalid_decoder_evaluation')
        if not isinstance(case['id'], str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', case['id']) or case['id'] in ids:
            raise engine.JobError('invalid_decoder_evaluation')
        if case['transform'] not in TRANSFORMS or case['payload'] is not None and not re.fullmatch('[01]{61}', case['payload']):
            raise engine.JobError('invalid_decoder_evaluation')
        if case['expectedReceiptId'] is not None and case['expectedReceiptId'] not in retained.values():
            raise engine.JobError('invalid_decoder_evaluation')
        ids.add(case['id'])
    return data


def source_image(seed=71):
    from PIL import Image, ImageDraw
    image = Image.new('RGB', (768, 1024), 'white')
    draw = ImageDraw.Draw(image)
    rng = random.Random(seed)
    for index in range(260):
        x, y = rng.randrange(20, 720), rng.randrange(20, 980)
        width, height = rng.randrange(10, 100), rng.randrange(8, 70)
        color = tuple(rng.randrange(20, 180) for _ in range(3))
        draw.rectangle((x, y, min(748, x + width), min(1004, y + height)), outline=color, width=2)
        if index % 8 == 0:
            draw.text((x, y), f'HHC {index:03d}', fill=color)
    return image


def jpeg_roundtrip(image, quality):
    from PIL import Image
    stream = io.BytesIO()
    image.save(stream, format='JPEG', quality=quality)
    stream.seek(0)
    with Image.open(stream) as decoded:
        decoded.load()
        return decoded.convert('RGB')


def transform(image, name, root):
    from PIL import Image, ImageDraw
    if name in {'png', 'unmarked', 'unrelated', 'unknown_payload', 'wrong_issue', 'copied_metadata', 'multi_receipt'}:
        return image.copy()
    if name == 'jpeg':
        return jpeg_roundtrip(image, 85)
    if name == 'scale':
        return image.resize((576, 768), Image.Resampling.LANCZOS)
    if name in {'crop', 'first_only'}:
        return image.crop((38, 51, 730, 973)).resize(image.size, Image.Resampling.LANCZOS)
    if name == 'rotation':
        return image.transpose(Image.Transpose.ROTATE_90)
    if name == 'screenshot':
        canvas = Image.new('RGB', (900, 1200), (235, 235, 235))
        canvas.paste(image.resize((768, 1024), Image.Resampling.LANCZOS), (66, 88))
        return canvas
    if name == 'line':
        return jpeg_roundtrip(image.resize((720, 960), Image.Resampling.LANCZOS), 70)
    if name == 'damaged':
        damaged = image.copy()
        ImageDraw.Draw(damaged).rectangle((0, 0, damaged.width, damaged.height // 2), fill='white')
        return damaged
    if name == 'pdf_raster':
        from reportlab.pdfgen import canvas
        png, pdf = root / 'page.png', root / 'page.pdf'
        image.save(png, format='PNG')
        writer = canvas.Canvas(str(pdf), pagesize=(image.width, image.height), pageCompression=0)
        writer.drawImage(str(png), 0, 0, image.width, image.height)
        writer.save()
        prefix = root / 'raster'
        subprocess.run([engine.os.environ.get('HHC_WATERMARK_PDFTOPPM', '/usr/bin/pdftoppm'), '-f', '1', '-singlefile', '-png', '-r', '72', str(pdf), str(prefix)], check=True, capture_output=True)
        with Image.open(root / 'raster.png') as raster:
            raster.load()
            return raster.convert('RGB')
    raise engine.JobError('invalid_decoder_transform')


def evaluate(path):
    data = load_manifest(path)
    engine.check_runtime()
    started = time.perf_counter()
    model = engine.load_encoder()
    cold = time.perf_counter() - started
    retained = data['retainedPayloads']
    base = source_image()
    marked = {}
    rows, decode_seconds = [], []
    batch_started = time.perf_counter()
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        for case in data['cases']:
            payload = case['payload']
            if case['transform'] == 'unrelated':
                image = source_image(99)
            elif payload is None:
                image = base
            else:
                if payload not in marked:
                    marked[payload] = model.encode(base, payload, MODE='binary', WM_STRENGTH=1.0)
                image = marked[payload]
            query = transform(image, case['transform'], root)
            started = time.perf_counter()
            decoded = engine.decode_image(model, query, float(data['minimumQuality']))
            elapsed = time.perf_counter() - started
            decode_seconds.append(elapsed)
            finding = classify(case['expectedReceiptId'], decoded.payload, retained)
            rows.append({'id': case['id'], 'transform': case['transform'], 'outcome': finding['outcome'],
                         'falseAttribution': finding['falseAttribution'], 'quality': decoded.quality,
                         'seconds': elapsed, **({'receiptId': finding['receiptId']} if 'receiptId' in finding else {})})
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    if engine.runtime_target() != 'macos-arm64':
        rss *= 1024
    return summarize(rows, float(data['minimumQuality']), {
        'coldLoadSeconds': cold,
        'warmMeanSeconds': sum(decode_seconds) / len(decode_seconds),
        'batchSeconds': time.perf_counter() - batch_started,
        'peakRssBytes': rss,
    })


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--manifest', required=True, type=Path)
    parser.add_argument('--report', type=Path)
    args = parser.parse_args()
    try:
        result = evaluate(args.manifest)
        if args.report:
            args.report.write_text(markdown(result))
        print(json.dumps(result, separators=(',', ':')))
        return 0
    except Exception:
        print('{"status":"failed","errorCode":"decoder_evaluation_failed"}')
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
