#!/opt/watermark/bin/python
"""Private subprocess entry point for PDF issuance and image investigations."""
import argparse
import contextlib
import os
import json
from pathlib import Path
import re
import resource
import struct
import sys

sys.path.insert(0, "/opt/watermark")

import engine
import pdf_engine

FRAME_LIMIT = 64 << 10
DIRECT_DECODE_MINIMUM_QUALITY = .9


def read_frame(stream):
    header = stream.read(4)
    if header == b'':
        return None
    if len(header) != 4:
        raise ValueError('partial frame')
    length = struct.unpack('>I', header)[0]
    if not 0 < length <= FRAME_LIMIT:
        raise ValueError('invalid frame size')
    body = stream.read(length)
    if len(body) != length:
        raise ValueError('partial frame')
    return json.loads(body)


def write_frame(stream, value):
    body = json.dumps(value, separators=(',', ':')).encode()
    if len(body) > FRAME_LIMIT:
        raise ValueError('response too large')
    stream.write(struct.pack('>I', len(body)) + body)
    stream.flush()


def decoder_request_path(job_root, request):
    if not isinstance(request, dict) or set(request) != {'job', 'evaluationIdentity'}:
        raise ValueError('invalid request')
    job = request['job']
    if request['evaluationIdentity'] != engine.DECODER_VERSION or not isinstance(job, str) or not re.fullmatch(r'decode-[A-Za-z0-9_-]{1,64}', job):
        raise ValueError('invalid request')
    directory = job_root / job
    if directory.is_symlink() or not directory.is_dir() or directory.stat().st_mode & 0o077:
        raise ValueError('invalid job')
    image = directory / 'input'
    if image.is_symlink() or not image.is_file():
        raise ValueError('invalid image')
    return image


def serve_decoder(job_root, input_stream, output_stream):
    if job_root.is_symlink() or not job_root.is_dir() or job_root.stat().st_mode & 0o077:
        raise ValueError('invalid root')
    with open(os.devnull, 'w') as quiet, contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
        model = engine.load_encoder()
    while True:
        request = read_frame(input_stream)
        if request is None:
            return
        try:
            image_path = decoder_request_path(job_root, request)
            with open(os.devnull, 'w') as quiet, contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
                image = engine.read_image(image_path)
                result = engine.decode_image(model, image, DIRECT_DECODE_MINIMUM_QUALITY)
            response = {'status': result.status, 'evaluationIdentity': engine.DECODER_VERSION, 'quality': result.quality}
            if result.status == 'decoded':
                response['payload'] = result.payload
        except Exception:
            response = {'status': 'failed', 'evaluationIdentity': engine.DECODER_VERSION}
        write_frame(output_stream, response)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--job-dir', type=Path)
    parser.add_argument('--decode-server', action='store_true')
    parser.add_argument('--job-root', type=Path)
    args = parser.parse_args()
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_FSIZE, (32 << 20, 32 << 20))
    resource.setrlimit(resource.RLIMIT_NOFILE, (128, 128))
    if args.decode_server:
        if args.job_dir is not None or args.job_root is None:
            return 2
        try:
            serve_decoder(args.job_root, sys.stdin.buffer, sys.stdout.buffer)
            return 0
        except Exception:
            return 1
    if args.job_dir is None or args.job_root is not None:
        return 2
    resource.setrlimit(resource.RLIMIT_CPU, (300, 300))
    try:
        request = engine.load_json(args.job_dir / 'request.json')
        runner = engine if request.get('operation') == 'detect' else pdf_engine
        with open(os.devnull, 'w') as quiet, contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
            result = runner.run_job(args.job_dir)
    except Exception:
        # Diagnostics may contain document content; only return a stable error.
        print('{"status":"failed","error_code":"processing_failed"}')
        return 1
    print(json.dumps(result, separators=(',', ':')))
    return 0


if __name__ == '__main__':
    sys.exit(main())
