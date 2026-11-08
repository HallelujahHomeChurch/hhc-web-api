"""Lossless soft-mask PDF watermark helper; original PDF objects stay intact."""
import argparse
import contextlib
import hashlib
import io
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import uuid

import engine

MAX_INPUT_BYTES = 20 * 1024 * 1024
MAX_OUTPUT_BYTES = 32 * 1024 * 1024
MAX_PAGES = 60
RASTER_WIDTH = 1080
PAGE_LOCATOR_WIDTH = 480
POPPLER_VERSIONS = {'macos-arm64': '26.05.0', 'linux-amd64': '25.03.0'}
POPPLER_VERSION = POPPLER_VERSIONS.get(engine.runtime_target(), 'unsupported')
PYPDF_VERSION = '6.21.4'
# Receipt-compatible rendering identity; installed pypdf is checked separately.
PDF_RENDERER_COMPATIBILITY_VERSION = '6.14.2'
PDF_ENGINE_VERSION = engine.ENGINE_VERSION + '-pdf-rgba3-background-1080-poppler' + POPPLER_VERSION + '-rl4.4.9-pypdf' + PDF_RENDERER_COMPATIBILITY_VERSION
PDF_SCOPE_METADATA_KEY = '/HHCBulletinScope'


def read_request(job_dir):
    request = engine.load_json(engine.resolve_job_file(job_dir, 'request.json'))
    if not isinstance(request, dict): raise engine.JobError('invalid_manifest')
    fields = {'render_pdf': ({'operation', 'source', 'output', 'payload'}, {'operation', 'source', 'output', 'payload', 'scope'}),
              'templates': ({'operation', 'source', 'query', 'candidates'}, {'operation', 'source', 'query', 'candidates', 'page'}),
              'locate': ({'operation', 'source', 'query'},),
              'query_pdf': ({'operation', 'source'},)}
    operation = request.get('operation')
    if not isinstance(operation, str) or operation not in fields: raise engine.JobError('invalid_operation')
    if set(request) not in fields[operation]: raise engine.JobError('invalid_manifest')
    engine.resolve_job_file(job_dir, request['source'])
    def payload(value):
        if not isinstance(value, str) or not re.fullmatch('[01]{61}', value):
            raise engine.JobError('invalid_payload')
    if operation == 'render_pdf':
        payload(request['payload'])
        output = engine.resolve_job_file(job_dir, request['output'])
        if output == engine.resolve_job_file(job_dir, request['source']) or output.name == 'request.json':
            raise engine.JobError('invalid_output')
        if 'scope' in request:
            scope = request['scope']
            if not valid_scope(scope):
                raise engine.JobError('invalid_scope')
    elif operation in {'templates', 'locate'}:
        engine.resolve_job_file(job_dir, request['query'])
    if operation == 'templates':
        if 'page' in request and (type(request['page']) is not int or not 1 <= request['page'] <= MAX_PAGES):
            raise engine.JobError('invalid_page')
        candidates = request['candidates']
        if not isinstance(candidates, list) or not 2 <= len(candidates) <= engine.MAX_CANDIDATES:
            raise engine.JobError('invalid_candidates')
        ids = set()
        codes = set()
        for candidate in candidates:
            if not isinstance(candidate, dict) or set(candidate) != {'id', 'payload'}:
                raise engine.JobError('invalid_candidate')
            ident = candidate['id']
            if not isinstance(ident, str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', ident) or ident in ids:
                raise engine.JobError('invalid_candidate_id')
            payload(candidate['payload'])
            if candidate['payload'] in codes: raise engine.JobError('duplicate_payload')
            ids.add(ident)
            codes.add(candidate['payload'])
    return request


def check_pdf_runtime():
    import importlib.metadata
    engine.check_runtime()
    try:
        if importlib.metadata.version('pypdf') != PYPDF_VERSION or importlib.metadata.version('reportlab') != '4.4.9':
            raise engine.JobError('pdf_runtime_mismatch')
    except importlib.metadata.PackageNotFoundError:
        raise engine.JobError('pdf_runtime_mismatch') from None
    executable = os.environ.get('HHC_WATERMARK_PDFTOPPM', '')
    path = Path(executable)
    if not executable or not path.is_absolute() or not path.is_file() or not os.access(path, os.X_OK):
        raise engine.JobError('rasterizer_unavailable')
    try:
        result = subprocess.run([str(path), '-v'], capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        raise engine.JobError('rasterizer_unavailable') from None
    if result.returncode or not re.search(r'pdftoppm version ' + re.escape(POPPLER_VERSION) + r'(?:\s|$)', result.stdout + result.stderr):
        raise engine.JobError('rasterizer_version_mismatch')
    return path


def read_pdf(path):
    from pypdf import PdfReader
    from pypdf.generic import IndirectObject, DictionaryObject, ArrayObject
    try:
        if not path.is_file() or not 8 <= path.stat().st_size <= MAX_INPUT_BYTES:
            raise engine.JobError('pdf_size_limit')
        data = path.read_bytes()
        if not data.startswith(b'%PDF-') or not data.rstrip().endswith(b'%%EOF') or b'startxref' not in data[-1024:]:
            raise engine.JobError('invalid_pdf')
        reader = PdfReader(io.BytesIO(data), strict=False)
        if reader.is_encrypted: raise engine.JobError('encrypted_pdf')
        if not 1 <= len(reader.pages) <= MAX_PAGES: raise engine.JobError('pdf_page_limit')
        pending, seen, count = [reader.trailer], set(), 0
        while pending:
            value = pending.pop()
            if isinstance(value, IndirectObject):
                key = (value.idnum, value.generation)
                if key in seen: continue
                seen.add(key)
                value = value.get_object()
            count += 1
            if count > 100000: raise engine.JobError('pdf_complexity_limit')
            if isinstance(value, DictionaryObject):
                if '/ByteRange' in value or value.get('/Type') == '/Sig' or value.get('/FT') == '/Sig':
                    raise engine.JobError('signed_pdf')
                pending.extend(value.values())
            elif isinstance(value, ArrayObject):
                pending.extend(value)
        for page in reader.pages:
            box = page.cropbox
            numbers = [float(v) for v in box]
            if not all(math.isfinite(v) for v in numbers) or box.width <= 0 or box.height <= 0:
                raise engine.JobError('invalid_page_geometry')
            if page.rotation % 90 or abs(page.rotation) > 360:
                raise engine.JobError('invalid_page_geometry')
            unit = float(page.get('/UserUnit', 1))
            if not math.isfinite(unit) or not 0 < unit <= 75000:
                raise engine.JobError('invalid_page_geometry')
            width, height = float(box.width), float(box.height)
            if page.rotation % 180: width, height = height, width
            if RASTER_WIDTH * math.ceil(RASTER_WIDTH * height / width) > engine.MAX_PIXELS:
                raise engine.JobError('image_pixel_limit')
        return reader, hashlib.sha256(data).hexdigest()
    except engine.JobError:
        raise
    except Exception:
        raise engine.JobError('invalid_pdf') from None


def raster_page(executable, pdf_path, page_number, stage, width=RASTER_WIDTH):
    import reportlab
    from xml.sax.saxutils import escape
    font_dir = Path(reportlab.__file__).parent/'fonts'
    font_config = stage/'fontconfig.xml'
    font_config.write_text(f'<fontconfig><dir>{escape(str(font_dir))}</dir><cachedir>{escape(str(stage/"font-cache"))}</cachedir></fontconfig>')
    environment = {'PATH': '/usr/bin:/bin', 'HOME': str(stage), 'TMPDIR': str(stage),
                   'FONTCONFIG_FILE': str(font_config), 'FONTCONFIG_PATH': str(stage),
                   'XDG_CACHE_HOME': str(stage/'font-cache')}
    prefix = stage/'raster'
    output = prefix.with_suffix('.ppm')
    try:
        result = subprocess.run([str(executable), '-f', str(page_number), '-l', str(page_number), '-singlefile',
                                 '-cropbox', '-scale-dimension-before-rotation', '-scale-to-x', str(width), '-scale-to-y', '-1',
                                 str(pdf_path), str(prefix)], capture_output=True, timeout=60, env=environment)
        if result.returncode: raise engine.JobError('pdf_raster_failed')
        # Internal PPM avoids compressing PNG twice; public image inputs remain PNG/JPEG.
        from PIL import Image
        if output.stat().st_size > engine.MAX_PIXELS*3+1024:
            raise engine.JobError('pdf_raster_size_limit')
        with Image.open(output) as image:
            if image.format != 'PPM' or image.mode != 'RGB': raise engine.JobError('pdf_raster_format')
            if image.width*image.height > engine.MAX_PIXELS: raise engine.JobError('image_pixel_limit')
            if image.width != width: raise engine.JobError('rendered_dimension_mismatch')
            image.load()
            return image.convert('RGB')
    except subprocess.TimeoutExpired:
        raise engine.JobError('pdf_raster_timeout') from None
    except OSError:
        raise engine.JobError('pdf_raster_failed') from None
    finally:
        output.unlink(missing_ok=True)


def png_bytes(image):
    stream = io.BytesIO()
    image.save(stream, format='PNG')
    result = stream.getvalue()
    if len(result) > engine.MAX_BYTES: raise engine.JobError('image_size_limit')
    return result


def rgba_residual(original, marked):
    import numpy as np
    from PIL import Image
    if original.size != marked.size: raise engine.JobError('encoded_dimension_mismatch')
    source = np.asarray(original, dtype=float)
    target = np.asarray(marked, dtype=float)
    delta = target-source
    upward = np.divide(delta, 255-source, out=np.zeros_like(delta), where=source < 255)
    downward = np.divide(-delta, source, out=np.zeros_like(delta), where=source > 0)
    alpha = np.ceil(np.maximum(np.maximum(upward, downward).max(axis=2), 0)*255)/255
    color = source + np.divide(delta, alpha[:, :, None], out=np.zeros_like(delta), where=alpha[:, :, None] > 0)
    color[alpha == 0] = 0
    rgba = np.dstack((np.clip(np.rint(color), 0, 255).astype('uint8'), np.rint(alpha*255).astype('uint8')))
    return Image.fromarray(rgba, 'RGBA')


def background_mark(model, original, payload):
    """Keep the learned residual off glyph, rule and image edges."""
    import cv2
    import numpy as np
    from PIL import Image
    source = np.asarray(original)
    encoded = np.asarray(model.encode(original, payload, MODE='binary', WM_STRENGTH=1.0))
    white = (np.min(source, axis=2) >= 250).astype(np.uint8)
    safe = cv2.erode(white, np.ones((15, 15), np.uint8)).astype(bool)
    marked = source.copy()
    marked[safe] = encoded[safe]
    return Image.fromarray(marked, 'RGB')


def overlay_page(page, original, marked):
    from pypdf import PdfReader
    from reportlab.lib.utils import ImageReader
    from reportlab.pdfgen.canvas import Canvas
    from reportlab import rl_config
    # ASCII85 adds 25% to already-compressed RGB/alpha streams without improving pixels.
    rl_config.useA85 = False
    overlay = rgba_residual(original, marked)
    # Poppler renders display rotation; undo only on the overlay, never on the PDF.
    if page.rotation % 360: overlay = overlay.rotate(page.rotation, expand=True)
    box = page.cropbox
    stream = io.BytesIO()
    canvas = Canvas(stream, pagesize=(float(box.width), float(box.height)), invariant=1, pageCompression=1)
    canvas.addLiteral('/Artifact BMC')
    canvas.drawImage(ImageReader(overlay), 0, 0, width=float(box.width), height=float(box.height), mask='auto')
    canvas.addLiteral('EMC')
    canvas.save()
    stream.seek(0)
    layer = PdfReader(stream).pages[0]
    from pypdf.generic import ArrayObject, DecodedStreamObject, DictionaryObject, NameObject, RectangleObject
    writer = page.indirect_reference.pdf
    if writer.pdf_header < '%PDF-1.4': writer.pdf_header = '%PDF-1.4'
    form = DecodedStreamObject()
    form.set_data(layer.get_contents().get_data())
    form.update({NameObject('/Type'): NameObject('/XObject'), NameObject('/Subtype'): NameObject('/Form'),
                 NameObject('/BBox'): RectangleObject((0, 0, float(box.width), float(box.height))),
                 NameObject('/Resources'): layer['/Resources']})
    form_ref = writer._add_object(form.clone(writer))
    resources = DictionaryObject(page.get('/Resources', DictionaryObject()).get_object())
    xobjects = DictionaryObject(resources.get('/XObject', DictionaryObject()).get_object())
    index = 0
    while f'/HHCWatermark{index}' in xobjects: index += 1
    name = NameObject(f'/HHCWatermark{index}')
    xobjects[name] = form_ref
    resources[NameObject('/XObject')] = xobjects
    page[NameObject('/Resources')] = resources
    existing = page.get('/Contents')
    objects = existing.get_object() if existing is not None else ArrayObject()
    contents = list(objects) if isinstance(objects, ArrayObject) else [existing]
    prefix, suffix = DecodedStreamObject(), DecodedStreamObject()
    prefix.set_data(b'q\n')
    suffix.set_data(f'\nQ\n/Artifact BMC\nq 1 0 0 1 {float(box.left):.12g} {float(box.bottom):.12g} cm {name} Do Q\nEMC\n'.encode('ascii'))
    page[NameObject('/Contents')] = ArrayObject([writer._add_object(prefix), *contents, writer._add_object(suffix)])


def write_pdf(writer, path):
    created = False
    try:
        with path.open('xb') as stream:
            created = True
            writer.write(stream)
            stream.flush()
            os.fsync(stream.fileno())
        path.chmod(0o600)
        if path.stat().st_size > MAX_OUTPUT_BYTES: raise engine.JobError('pdf_output_size_limit')
    except engine.JobError:
        if created: path.unlink(missing_ok=True)
        raise
    except Exception:
        if created: path.unlink(missing_ok=True)
        raise engine.JobError('pdf_write_failed') from None


def publish_files(stage, job_dir, names):
    destinations = [engine.resolve_job_file(job_dir, name) for name in names]
    if any(path.exists() or not path.parent.is_dir() for path in destinations):
        raise engine.JobError('output_exists')
    published = []
    try:
        for name, destination in zip(names, destinations):
            (stage/name).chmod(0o600)
            os.link(stage/name, destination)
            published.append(destination)
    except OSError:
        for path in published: path.unlink(missing_ok=True)
        raise engine.JobError('output_write_failed') from None


def valid_scope(value):
    try:
        if not isinstance(value, dict) or set(value) != {'version', 'issueId', 'series', 'locale', 'revision', 'sourceSha256', 'rendererVersion'}:
            return False
        if value['version'] != 1 or type(value['revision']) is not int or not 1 <= value['revision'] <= 2**63-1:
            return False
        if not all(isinstance(value[field], str) for field in ('issueId', 'series', 'locale', 'sourceSha256', 'rendererVersion')):
            return False
        if str(uuid.UUID(value['issueId'])) != value['issueId'] or (value['series'], value['locale']) not in {
                ('general', 'zh-Hant'), ('general', 'zh-Hans'), ('general', 'en'),
                ('children', 'zh-Hant'), ('children', 'en')}:
            return False
        if not re.fullmatch('[a-f0-9]{64}', value['sourceSha256']) or not re.fullmatch('[A-Za-z0-9._:+-]{1,200}', value['rendererVersion']):
            return False
        return True
    except (ValueError, TypeError, KeyError):
        return False


def scope_hint(reader):
    metadata = reader.metadata
    raw = metadata.get(PDF_SCOPE_METADATA_KEY) if metadata is not None else None
    if not isinstance(raw, str) or len(raw) > 2048:
        return None
    try:
        value = json.loads(raw)
    except (ValueError, TypeError):
        return None
    return value if valid_scope(value) else None


def write_phase_marks(job_dir, marks):
    """Best-effort wall-clock stage markers for worker timing logs only."""
    try:
        (job_dir/'phases').write_text(json.dumps(marks, separators=(',', ':')))
    except OSError:
        pass


def render_pdf(job_dir, request, reader, source_hash, executable, stage):
    marks = {'ready': time.time()}
    from pypdf import PdfWriter
    output = engine.resolve_job_file(job_dir, request['output'])
    if output.exists() or not output.parent.is_dir(): raise engine.JobError('output_exists')
    source = engine.resolve_job_file(job_dir, request['source'])
    writer = PdfWriter()
    writer.clone_document_from_reader(reader)
    if 'scope' in request:
        writer.add_metadata({PDF_SCOPE_METADATA_KEY: json.dumps(request['scope'], separators=(',', ':'))})
    model = engine.load_encoder()
    marks['model'] = time.time()
    pages = []
    total = len(writer.pages)
    for number, page in enumerate(writer.pages, 1):
        original = raster_page(executable, source, number, stage)
        source_png = png_bytes(original)
        marked = background_mark(model, original, request['payload'])
        overlay_page(page, original, marked)
        pages.append({'page': number, 'width': original.width, 'height': original.height,
                      'source_sha256': hashlib.sha256(source_png).hexdigest()})
        (job_dir/'progress').write_text(str(15 + round(50 * number / total)))
    temporary = stage/'completed.pdf'
    write_pdf(writer, temporary)
    marks['encoded'] = time.time()
    for metadata in pages:
        image = raster_page(executable, temporary, metadata['page'], stage)
        if image.size != (metadata['width'], metadata['height']): raise engine.JobError('rendered_dimension_mismatch')
        metadata['output_sha256'] = hashlib.sha256(png_bytes(image)).hexdigest()
        (job_dir/'progress').write_text(str(65 + round(15 * metadata['page'] / total)))
    output_hash = engine.sha256(temporary)
    marks['verified'] = time.time()
    write_phase_marks(job_dir, marks)
    # Output names may contain a pre-existing private subdirectory.
    try:
        os.link(temporary, output)
    except OSError:
        raise engine.JobError('output_write_failed') from None
    return {'status': 'encoded_pdf', 'source_sha256': source_hash, 'output_sha256': output_hash,
            'engine_version': engine.ENGINE_VERSION, 'pdf_engine_version': PDF_ENGINE_VERSION,
            'model_sha256': engine.MODEL_SHA256, 'pages': pages}


def templates(job_dir, request, reader, source_hash, executable, stage):
    from pypdf import PdfWriter
    query = engine.read_image(engine.resolve_job_file(job_dir, request['query']))
    number = request.get('page')
    if number is None:
        previews = [raster_page(executable, engine.resolve_job_file(job_dir, request['source']), page, stage, PAGE_LOCATOR_WIDTH)
                    for page in range(1, len(reader.pages) + 1)]
        index = engine.locate_page(query, previews)
        if index is None:
            return {'status': 'inconclusive', 'pdf_source_sha256': source_hash,
                    'pdf_engine_version': PDF_ENGINE_VERSION, 'model_sha256': engine.MODEL_SHA256}
        number = index + 1
    elif number > len(reader.pages):
        raise engine.JobError('invalid_page')
    names = ['source.png'] + [f'candidate-{i}.png' for i in range(len(request['candidates']))]
    if any(engine.resolve_job_file(job_dir, name).exists() for name in names): raise engine.JobError('output_exists')
    source = engine.resolve_job_file(job_dir, request['source'])
    original = raster_page(executable, source, number, stage)
    if original.width * original.height * len(request['candidates']) > engine.MAX_CANDIDATE_PIXELS:
        raise engine.JobError('candidate_pixel_limit')
    source_png = png_bytes(original)
    (stage/'source.png').write_bytes(source_png)
    page_hash = hashlib.sha256(source_png).hexdigest()
    model = engine.load_encoder()
    candidates = []
    for index, candidate in enumerate(request['candidates']):
        marked = background_mark(model, original, candidate['payload'])
        writer = PdfWriter()
        writer.clone_document_from_reader(reader)
        overlay_page(writer.pages[number-1], original, marked)
        temporary = stage/'candidate.pdf'
        write_pdf(writer, temporary)
        rendered = raster_page(executable, temporary, number, stage)
        temporary.unlink()
        if rendered.size != original.size: raise engine.JobError('rendered_dimension_mismatch')
        data = png_bytes(rendered)
        filename = names[index+1]
        (stage/filename).write_bytes(data)
        candidates.append({'id': candidate['id'], 'image': filename, 'source_sha256': page_hash,
                           'output_sha256': hashlib.sha256(data).hexdigest(),
                           'engine_version': engine.ENGINE_VERSION, 'model_sha256': engine.MODEL_SHA256})
    publish_files(stage, job_dir, names)
    return {'status': 'templates', 'source': 'source.png', 'source_sha256': page_hash,
            'pdf_source_sha256': source_hash, 'page': number, 'width': original.width, 'height': original.height,
            'engine_version': engine.ENGINE_VERSION, 'pdf_engine_version': PDF_ENGINE_VERSION,
            'model_sha256': engine.MODEL_SHA256, 'candidates': candidates}


def query_pdf(job_dir, request, reader, executable, stage):
    source = engine.resolve_job_file(job_dir, request['source'])
    names = []
    for number in range(1, len(reader.pages) + 1):
        name = f'query-{number:02d}.png'
        (stage/name).write_bytes(png_bytes(raster_page(executable, source, number, stage)))
        names.append(name)
    publish_files(stage, job_dir, names)
    return {'status': 'query_pdf', 'images': names, 'scope_hint': scope_hint(reader)}


def locate(job_dir, request, reader, source_hash, executable, stage):
    query = engine.read_image(engine.resolve_job_file(job_dir, request['query']))
    source = engine.resolve_job_file(job_dir, request['source'])
    previews = [raster_page(executable, source, number, stage, PAGE_LOCATOR_WIDTH)
                for number in range(1, len(reader.pages) + 1)]
    index = engine.locate_page(query, previews)
    return {'status': 'located' if index is not None else 'inconclusive',
            'page': index + 1 if index is not None else 0,
            'pdf_source_sha256': source_hash, 'pdf_engine_version': PDF_ENGINE_VERSION,
            'model_sha256': engine.MODEL_SHA256}


def run_job(job_dir):
    if job_dir.is_symlink() or not job_dir.is_dir() or job_dir.stat().st_mode & 0o077:
        raise engine.JobError('job_directory_not_private')
    request = read_request(job_dir)
    executable = check_pdf_runtime()
    reader, source_hash = read_pdf(engine.resolve_job_file(job_dir, request['source']))
    with tempfile.TemporaryDirectory(prefix='.pdf-watermark-', dir=job_dir) as folder:
        stage = Path(folder)
        if request['operation'] == 'render_pdf':
            return render_pdf(job_dir, request, reader, source_hash, executable, stage)
        if request['operation'] == 'templates':
            return templates(job_dir, request, reader, source_hash, executable, stage)
        if request['operation'] == 'locate':
            return locate(job_dir, request, reader, source_hash, executable, stage)
        return query_pdf(job_dir, request, reader, executable, stage)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--job-dir', type=Path, required=True)
    args = parser.parse_args()
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            result = run_job(args.job_dir)
        code = 0
    except engine.JobError as error:
        result, code = {'status': 'failed', 'error_code': str(error)}, 1
    except ImportError:
        result, code = {'status': 'failed', 'error_code': 'dependency_unavailable'}, 1
    except Exception:
        result, code = {'status': 'failed', 'error_code': 'pdf_engine_error'}, 1
    print(json.dumps(result, separators=(',', ':')))
    return code


if __name__ == '__main__': sys.exit(main())
