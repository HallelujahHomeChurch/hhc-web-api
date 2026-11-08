"""Offline synthetic bulletin watermark jobs. No CMS or account integration."""
import argparse
import contextlib
from dataclasses import dataclass
import hashlib
import io
import json
import os
import platform
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
from typing import Optional

def runtime_target():
    return {('Darwin', 'arm64'): 'macos-arm64', ('Linux', 'x86_64'): 'linux-amd64'}.get((platform.system(), platform.machine()), 'unsupported')


ENGINE_VERSION = 'trustmark-q1-bch5-cpu-ecc1-' + runtime_target() + '-py3.12.14-runtime1'
DETECTOR_VERSION = 'sift-ecc-white250-radius7-globalfit-corr090-gap015-v2'
DECODER_VERSION = 'trustmark-q1-bch5-logitmean-rot4-v1'
RUNTIME_PACKAGES = {'numpy': '2.5.3', 'Pillow': '13.0.0', 'opencv-python-headless': '5.0.0.93',
                    'torch': '2.14.0', 'torchvision': '0.29.0', 'trustmark': '0.9.2'}
MODEL_FILES = {
    'trustmark_Q.yaml': '43f37103f92efa8bd6b1c5902bb537cc12a981dc699fca19d9bb7de8c62d03d9',
    'encoder_Q.ckpt': 'dc382c3f6b4fd568b27d6fbb763d6ffc2d2587126d84afe9d5ee95b4c5d99826',
    'decoder_Q.ckpt': 'e3d9cea5406a26590735719f8f15cb10802b11852ae69047eaf4cf17214df781',
}
MODEL_SHA256 = hashlib.sha256(''.join(f'{name} {MODEL_FILES[name]}\n' for name in sorted(MODEL_FILES)).encode('ascii')).hexdigest()
MAX_BYTES = 10 * 1024 * 1024
MAX_PIXELS = 20_000_000
MAX_CANDIDATES = 64
MAX_CANDIDATE_PIXELS = 128_000_000


class JobError(Exception):
    pass


@dataclass(frozen=True)
class DecodeResult:
    status: str
    payload: Optional[str]
    quality: Optional[float]


def resolve_job_file(job_dir: Path, name: str) -> Path:
    if not isinstance(name, str) or not name or '\\' in name or ':' in name:
        raise JobError('invalid_path')
    path = Path(name)
    if path.is_absolute() or any(p in ('.', '..', '') for p in name.split('/')):
        raise JobError('invalid_path')
    root = job_dir.resolve()
    current = root
    for part in path.parts:
        current = current / part
        if current.is_symlink():
            raise JobError('symlink_rejected')
    resolved = current.resolve()
    if root not in resolved.parents:
        raise JobError('invalid_path')
    return resolved


def load_json(path, limit=1024*1024):
    try:
        if not path.is_file() or path.stat().st_size > limit:
            raise JobError('invalid_manifest')
        def unique(pairs):
            result = {}
            for key, value in pairs:
                if key in result: raise JobError('invalid_manifest')
                result[key] = value
            return result
        return json.loads(path.read_text(), object_pairs_hook=unique)
    except (OSError, ValueError, UnicodeError):
        raise JobError('invalid_manifest') from None


def read_request(job_dir: Path) -> dict:
    request = load_json(resolve_job_file(job_dir, 'request.json'))
    if not isinstance(request, dict): raise JobError('invalid_manifest')
    operation = request.get('operation')
    fields = {'encode': {'operation', 'source', 'payload', 'output'},
              'detect': {'operation', 'source', 'query', 'candidates'}}
    if not isinstance(operation, str) or operation not in fields: raise JobError('invalid_operation')
    if set(request) != fields[operation]: raise JobError('invalid_manifest')
    resolve_job_file(job_dir, request['source'])
    if operation == 'encode':
        if not isinstance(request['payload'], str) or not re.fullmatch('[01]{61}', request['payload']):
            raise JobError('invalid_payload')
        resolve_job_file(job_dir, request['output'])
        if request['output'] in (request['source'], 'request.json'): raise JobError('invalid_output')
    else:
        resolve_job_file(job_dir, request['query'])
        candidates = request['candidates']
        if not isinstance(candidates, list) or not 2 <= len(candidates) <= MAX_CANDIDATES:
            raise JobError('invalid_candidates')
        ids = set()
        for candidate in candidates:
            if not isinstance(candidate, dict) or set(candidate) != {'id', 'image', 'source_sha256', 'output_sha256', 'engine_version', 'model_sha256'}:
                raise JobError('invalid_candidate')
            ident = candidate['id']
            if not isinstance(ident, str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', ident) or ident in ids:
                raise JobError('invalid_candidate_id')
            ids.add(ident)
            resolve_job_file(job_dir, candidate['image'])
            for field in ('source_sha256', 'output_sha256', 'model_sha256'):
                if not isinstance(candidate[field], str) or not re.fullmatch('[0-9a-f]{64}', candidate[field]):
                    raise JobError('invalid_candidate_hash')
            if candidate['engine_version'] != ENGINE_VERSION or candidate['model_sha256'] != MODEL_SHA256:
                raise JobError('candidate_version_mismatch')
    return request


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024*1024), b''): digest.update(chunk)
    return digest.hexdigest()


def read_image(path):
    from PIL import Image
    try:
        if not path.is_file() or path.stat().st_size > MAX_BYTES: raise JobError('image_size_limit')
        with Image.open(path) as image:
            if image.format not in ('PNG', 'JPEG'): raise JobError('unsupported_image')
            if image.width * image.height > MAX_PIXELS: raise JobError('image_pixel_limit')
            image.load()
            return image.convert('RGB')
    except (OSError, ValueError, Image.DecompressionBombError):
        raise JobError('invalid_image') from None


def check_runtime():
    import platform
    import importlib.metadata
    if platform.python_implementation() != 'CPython' or platform.python_version() != '3.12.14' or runtime_target() == 'unsupported':
        raise JobError('runtime_mismatch')
    try:
        packages = dict(RUNTIME_PACKAGES)
        if runtime_target() == 'linux-amd64':
            packages.update(torch='2.14.0+cpu', torchvision='0.29.0+cpu')
        if any(importlib.metadata.version(name) != version for name, version in packages.items()):
            raise JobError('runtime_mismatch')
    except importlib.metadata.PackageNotFoundError:
        raise JobError('runtime_mismatch') from None


def load_encoder():
    location = os.environ.get('HHC_WATERMARK_MODEL_DIR')
    if not location: raise JobError('model_unavailable')
    directory = Path(location)
    if directory.is_symlink() or not directory.is_dir() or directory.stat().st_mode & 0o222:
        raise JobError('model_not_readonly')
    for name, digest in MODEL_FILES.items():
        path = directory / name
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o222:
            raise JobError('model_not_readonly')
        if sha256(path) != digest: raise JobError('model_hash_mismatch')
    import torch
    from trustmark import TrustMark
    torch.set_num_threads(4)
    torch.set_grad_enabled(False)
    torch.manual_seed(0)
    torch.use_deterministic_algorithms(True)

    class OfflineTrustMark(TrustMark):
        def check_and_download(self, filepath):
            path = Path(filepath)
            if path.parent.resolve() != directory.resolve() or path.name not in MODEL_FILES or sha256(path) != MODEL_FILES[path.name]:
                raise JobError('model_hash_mismatch')

        def load_model(self, config_path, weight_path, device, secret_len, part='all'):
            return super().load_model(str(directory / Path(config_path).name), str(directory / Path(weight_path).name), device, secret_len, part)

    model = OfflineTrustMark(model_type='Q', device='cpu', use_ECC=True,
                            encoding_type=TrustMark.Encoding.BCH_5, secret_len=100,
                            loadRemover=False, loadBBoxDetector=False, verbose=False)
    if model.encoder is None or model.decoder is None: raise JobError('model_load_failed')
    return model


def _decode_with_quality(model, image):
    import torch
    from PIL import Image
    from torchvision import transforms
    best_quality = 0.0
    for rotated in (image, image.transpose(Image.Transpose.ROTATE_90),
                    image.transpose(Image.Transpose.ROTATE_180), image.transpose(Image.Transpose.ROTATE_270)):
        prepared = model.get_the_image_for_processing(rotated).resize(
            (model.model_resolution_dec, model.model_resolution_dec), Image.Resampling.BILINEAR)
        tensor = transforms.ToTensor()(prepared).unsqueeze(0).to(model.decoder.device) * 2.0 - 1.0
        with torch.no_grad():
            logits = model.decoder.decoder(tensor)
        quality = float(torch.sigmoid(logits.abs()).mean().item())
        bits = (logits > 0).cpu().numpy()
        payload, detected, _ = model.ecc.decode_bitstream(bits, 'binary')[0]
        best_quality = max(best_quality, quality)
        if detected:
            return payload, True, quality
    return '', False, best_quality


def decode_image(model, image, minimum_quality):
    if type(minimum_quality) is not float or not .5 <= minimum_quality <= 1:
        raise JobError('invalid_decode_threshold')
    payload, detected, quality = _decode_with_quality(model, image)
    if detected and isinstance(payload, str) and re.fullmatch('[01]{61}', payload) and quality >= minimum_quality:
        return DecodeResult('decoded', payload, quality)
    return DecodeResult('inconclusive', None, quality)


def encode(job_dir, request):
    output = resolve_job_file(job_dir, request['output'])
    if output.exists(): raise JobError('output_exists')
    if not output.parent.is_dir(): raise JobError('invalid_output')
    source = resolve_job_file(job_dir, request['source'])
    check_runtime()
    image = read_image(source)
    source_hash = sha256(source)
    model = load_encoder()
    marked = model.encode(image, request['payload'], MODE='binary', WM_STRENGTH=1.0)
    temporary = None
    try:
        fd, temporary = tempfile.mkstemp(prefix='.watermark-', suffix='.png', dir=output.parent)
        with os.fdopen(fd, 'wb') as stream:
            marked.save(stream, format='PNG')
            stream.flush()
            os.fsync(stream.fileno())
        if Path(temporary).stat().st_size > MAX_BYTES:
            raise JobError('output_size_limit')
        digest = sha256(Path(temporary))
        # Hard-link publishes atomically and cannot overwrite a competing file.
        os.link(temporary, output)
        return dict(status='encoded', source_sha256=source_hash, output_sha256=digest,
                    engine_version=ENGINE_VERSION, model_sha256=MODEL_SHA256)
    except OSError:
        raise JobError('output_write_failed') from None
    finally:
        if temporary is not None: Path(temporary).unlink(missing_ok=True)


def gray(image):
    import cv2
    import numpy as np
    return cv2.cvtColor(np.asarray(image), cv2.COLOR_RGB2GRAY)


def locate_quality(query, original):
    import cv2
    import numpy as np
    sift = cv2.SIFT_create(nfeatures=5000)
    kq, dq = sift.detectAndCompute(gray(query), None)
    kr, dr = sift.detectAndCompute(gray(original), None)
    if dq is None or dr is None: return None, 0, 0
    matches = cv2.BFMatcher().knnMatch(dq, dr, k=2)
    good = [pair[0] for pair in matches if len(pair) == 2 and pair[0].distance < .70 * pair[1].distance]
    if len(good) < 10: return None, len(good), 0
    a = np.float32([kq[m.queryIdx].pt for m in good])
    b = np.float32([kr[m.trainIdx].pt for m in good])
    matrix, inliers = cv2.estimateAffinePartial2D(a, b, method=cv2.RANSAC, ransacReprojThreshold=1.5)
    if matrix is None: return None, 0, 0
    count = int(inliers.sum())
    if count < 10: return None, count, count / len(good)
    scale = float(np.hypot(matrix[0, 0], matrix[0, 1]))
    if not np.isfinite(matrix).all() or not .1 <= scale <= 10: return None, count, count / len(good)
    return matrix, count, count / len(good)


def locate(query, original):
    return locate_quality(query, original)[0]


def choose_unique_page(scores):
    eligible = sorted((score for score in scores if score[1] >= 10 and score[2] >= .5),
                      key=lambda score: (score[1], score[2]), reverse=True)
    if not eligible: return None
    if len(eligible) == 1: return eligible[0][0]
    best, second = eligible[:2]
    return best[0] if best[1] >= max(second[1] + 8, (second[1] * 3 + 1) // 2) else None


def locate_page(query, pages):
    scores = []
    for index, page in enumerate(pages):
        _, inliers, ratio = locate_quality(query, page)
        scores.append((index, inliers, ratio))
    return choose_unique_page(scores)


def affine_fit(prediction, observed, mask=None):
    x = prediction[::3, ::3].astype(float).ravel()
    y = observed[::3, ::3].astype(float).ravel()
    xm, ym = x.mean(), y.mean()
    variance = np.mean((x-xm)**2)
    gain = float(np.mean((x-xm)*(y-ym))/max(variance, 1e-9))
    offset = float(ym-gain*xm)
    gain, offset = float(np.clip(gain, .85, 1.15)), float(np.clip(offset, -12, 12))
    modeled = np.clip(prediction.astype(float)*gain+offset, 0, 255)
    error = (modeled-observed)**2 if mask is None else (modeled[mask]-observed[mask])**2
    return float(np.mean(error)), gain, offset


def jpeg(image, quality):
    from PIL import Image
    stream = io.BytesIO()
    image.save(stream, format='JPEG', quality=quality)
    stream.seek(0)
    return Image.open(stream).convert('RGB')


def background_mask(original, matrix, size):
    # Source-only selection avoids favoring a candidate or observed watermark.
    if not np.isfinite(matrix).all(): return None
    determinant = float(np.linalg.det(matrix[:, :2]))
    if not np.isfinite(determinant) or determinant <= 0: return None
    scaled_radius = 7 / np.sqrt(determinant)
    if not np.isfinite(scaled_radius): return None
    radius = max(1, int(round(scaled_radius)))
    if radius > 64: return None
    white = (np.min(np.asarray(original), axis=2) >= 250).astype(np.uint8)
    mask = cv2.warpAffine(white, matrix, size, flags=cv2.INTER_NEAREST | cv2.WARP_INVERSE_MAP,
                          borderMode=cv2.BORDER_CONSTANT, borderValue=0)
    mask = cv2.erode(mask, np.ones((radius*2+1, radius*2+1), np.uint8)).astype(bool)
    if mask.mean() < .05 or mask.sum() < 1000: return None
    return mask


def choose_correlation(scores):
    if len(scores) < 2: return None
    ranking = np.argsort(scores)[::-1]
    best, second = float(scores[ranking[0]]), float(scores[ranking[1]])
    return int(ranking[0]) if best >= .90 and best-second >= .15 else None


def combine_matches(mse, correlation):
    if mse is not None and correlation is not None and mse != correlation: return None
    return mse if mse is not None else correlation


def identify_correlation(query, original, candidates, matrix):
    from PIL import Image
    def project(image):
        pixels = cv2.warpAffine(np.asarray(image), matrix, query.size,
                                flags=cv2.INTER_LINEAR | cv2.WARP_INVERSE_MAP,
                                borderMode=cv2.BORDER_CONSTANT, borderValue=(255, 255, 255))
        return Image.fromarray(pixels)
    def smooth_gray(image):
        pixels = np.asarray(image).astype(np.float32).mean(axis=2)
        return cv2.GaussianBlur(pixels, (0, 0), 1.2)
    def modeled(prediction, observed):
        _, gain, offset = affine_fit(prediction, observed)
        return np.clip(prediction.astype(np.float32)*gain+offset, 0, 255)
    def correlation(actual, expected):
        actual = actual.astype(np.float32)-float(actual.mean())
        expected = expected.astype(np.float32)-float(expected.mean())
        denominator = float(np.linalg.norm(actual)*np.linalg.norm(expected))
        return float(np.dot(actual, expected)/denominator) if denominator > 1e-6 else 0

    white = (np.min(np.asarray(original), axis=2) >= 250).astype(np.uint8)
    mask = cv2.warpAffine(white, matrix, query.size, flags=cv2.INTER_NEAREST | cv2.WARP_INVERSE_MAP,
                          borderMode=cv2.BORDER_CONSTANT, borderValue=0)
    mask = cv2.erode(mask, np.ones((7, 7), np.uint8)).astype(bool)
    if mask.sum() < 1000: return None
    observed = smooth_gray(query)
    projected_source = project(original)
    options = []
    for quality in [0, 50, 60, 70, 80, 90, 95]:
        base = projected_source if not quality else jpeg(projected_source, quality)
        fitted = modeled(smooth_gray(base), observed)
        options.append((float(np.mean((fitted[mask]-observed[mask])**2)), quality, fitted))
    _, quality, modeled_base = min(options)
    actual = observed[mask]-modeled_base[mask]
    scores = []
    for candidate in candidates:
        image = project(candidate)
        if quality: image = jpeg(image, quality)
        fitted = modeled(smooth_gray(image), observed)
        scores.append(correlation(actual, (fitted-modeled_base)[mask]))
    return choose_correlation(scores)


def identify_ecc(query, original, candidates):
    from PIL import Image
    matrix = locate(query, original)
    if matrix is None: return None
    matrix = matrix.astype(np.float32)
    try:
        _, matrix = cv2.findTransformECC(gray(query).astype(np.float32)/255, gray(original).astype(np.float32)/255,
                                        matrix, cv2.MOTION_AFFINE,
                                        (cv2.TERM_CRITERIA_COUNT | cv2.TERM_CRITERIA_EPS, 100, 1e-5), None, 5)
    except cv2.error: return None
    mask = background_mask(original, matrix, query.size)
    if mask is None: return None
    def project(image, interpolation):
        pixels = cv2.warpAffine(np.asarray(image), matrix, query.size, flags=interpolation | cv2.WARP_INVERSE_MAP,
                                borderMode=cv2.BORDER_CONSTANT, borderValue=(255, 255, 255))
        return Image.fromarray(pixels)
    def smooth(pixels): return cv2.GaussianBlur(pixels.astype(np.float32), (0, 0), 1.2)
    observed, options = smooth(np.asarray(query)), []
    for interpolation in [cv2.INTER_LINEAR, cv2.INTER_CUBIC, cv2.INTER_LANCZOS4]:
        base = project(original, interpolation)
        for quality in [0] + list(range(40, 96)):
            prediction = np.asarray(base if not quality else jpeg(base, quality))
            error, _, _ = affine_fit(smooth(prediction), observed, mask)
            options.append((error, interpolation, quality))
    baseline, interpolation, quality = min(options)
    errors = []
    # ponytail: linear search capped at 64; measure before raising this ceiling.
    for candidate in candidates:
        image = project(candidate, interpolation)
        prediction = np.asarray(image if not quality else jpeg(image, quality))
        error, _, _ = affine_fit(smooth(prediction), observed, mask)
        errors.append(error)
    ranking = np.argsort(errors)
    best, second = [errors[i] for i in ranking[:2]]
    improvement = (baseline-best)/max(baseline, 1e-6)
    gap = (second-best)/max(second, 1e-6)
    accepted = baseline > .01 and best <= 1 and improvement >= .8 and gap >= .1
    mse = int(ranking[0]) if accepted else None
    return combine_matches(mse, identify_correlation(query, original, candidates, matrix))


def detect(job_dir, request):
    global cv2, np
    check_runtime()
    import cv2
    import numpy as np
    cv2.setNumThreads(2)
    cv2.setRNGSeed(0)
    source = resolve_job_file(job_dir, request['source'])
    original = read_image(source)
    if original.width * original.height * len(request['candidates']) > MAX_CANDIDATE_PIXELS:
        raise JobError('candidate_pixel_limit')
    query = read_image(resolve_job_file(job_dir, request['query']))
    source_hash = sha256(source)
    images = []
    for candidate in request['candidates']:
        path = resolve_job_file(job_dir, candidate['image'])
        if candidate['source_sha256'] != source_hash: raise JobError('candidate_source_mismatch')
        image = read_image(path)
        if sha256(path) != candidate['output_sha256']: raise JobError('candidate_hash_mismatch')
        if image.size != original.size: raise JobError('candidate_dimension_mismatch')
        images.append(image)
    index = identify_ecc(query, original, images)
    return dict(status='inconclusive', detector_version=DETECTOR_VERSION) if index is None else dict(
        status='matched', candidate_id=request['candidates'][index]['id'], detector_version=DETECTOR_VERSION)


def run_job(job_dir):
    if job_dir.is_symlink() or not job_dir.is_dir() or job_dir.stat().st_mode & 0o077:
        raise JobError('job_directory_not_private')
    request = read_request(job_dir)
    return encode(job_dir, request) if request['operation'] == 'encode' else detect(job_dir, request)


def evaluate(manifest):
    data = load_json(manifest)
    if not isinstance(data, dict) or set(data) - {'cases', 'timeout_seconds'} or not isinstance(data.get('cases'), list) or not 1 <= len(data['cases']) <= 10000:
        raise JobError('invalid_evaluation')
    timeout = data.get('timeout_seconds', 300)
    if type(timeout) not in (int, float) or not 1 <= timeout <= 3600: raise JobError('invalid_evaluation')
    ids = set()
    for case in data['cases']:
        if not isinstance(case, dict) or set(case) != {'id', 'job_dir', 'expected_candidate_id'}: raise JobError('invalid_evaluation')
        ident, expected = case['id'], case['expected_candidate_id']
        if not isinstance(ident, str) or not re.fullmatch('[A-Za-z0-9_-]{1,120}', ident) or ident in ids: raise JobError('invalid_evaluation')
        if expected is not None and (not isinstance(expected, str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', expected)): raise JobError('invalid_evaluation')
        ids.add(ident)
        resolve_job_file(manifest.parent, case['job_dir'])
    results = []
    for case in data['cases']:
        start = time.perf_counter()
        try:
            process = subprocess.run([sys.executable, str(Path(__file__).resolve()), '--job-dir', str(resolve_job_file(manifest.parent, case['job_dir']))],
                                     capture_output=True, text=True, timeout=timeout)
            result = json.loads(process.stdout)
            if not isinstance(result, dict) or result.get('status') not in ('matched', 'inconclusive', 'failed'):
                raise ValueError()
            status = result['status']
            expected_fields = {'matched': {'status', 'candidate_id'}, 'inconclusive': {'status'}, 'failed': {'status', 'error_code'}}
            if set(result) != expected_fields[status] or bool(process.returncode) != (status == 'failed'): raise ValueError()
            if status == 'matched' and (not isinstance(result['candidate_id'], str) or not re.fullmatch('[A-Za-z0-9_-]{1,80}', result['candidate_id'])): raise ValueError()
            if status == 'failed' and (not isinstance(result['error_code'], str) or not re.fullmatch('[a-z_]{1,80}', result['error_code'])): raise ValueError()
        except subprocess.TimeoutExpired:
            result = {'status': 'failed', 'error_code': 'job_timeout'}
        except (OSError, ValueError):
            result = {'status': 'failed', 'error_code': 'invalid_worker_result'}
        actual = result.get('candidate_id')
        results.append(dict(id=case['id'], **result, seconds=time.perf_counter()-start,
                            correct=result['status'] != 'failed' and actual == case['expected_candidate_id'],
                            false_attribution=result['status'] == 'matched' and actual != case['expected_candidate_id']))
    return {'status': 'evaluated', 'cases': results}


def main():
    parser = argparse.ArgumentParser()
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument('--job-dir', type=Path)
    group.add_argument('--evaluate', type=Path)
    args = parser.parse_args()
    try:
        # Upstream dependencies print during load; no payload or diagnostics escape.
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            result = evaluate(args.evaluate) if args.evaluate else run_job(args.job_dir)
        code = int(result['status'] == 'failed' or any(c['status'] == 'failed' for c in result.get('cases', [])))
    except JobError as error:
        result, code = {'status': 'failed', 'error_code': str(error)}, 1
    except ImportError:
        result, code = {'status': 'failed', 'error_code': 'dependency_unavailable'}, 1
    except Exception:
        result, code = {'status': 'failed', 'error_code': 'engine_error'}, 1
    print(json.dumps(result, separators=(',', ':')))
    return code


if __name__ == '__main__': sys.exit(main())
