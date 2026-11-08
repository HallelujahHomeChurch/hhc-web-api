import hashlib
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from tools.bulletin_watermark.download_models import download


class DownloadModelsTest(unittest.TestCase):
    def test_retries_timed_out_download(self):
        payload = b"verified model"

        class Handler(BaseHTTPRequestHandler):
            requests = 0

            def do_GET(self):
                type(self).requests += 1
                if type(self).requests == 1:
                    time.sleep(0.05)
                self.send_response(200)
                self.end_headers()
                try:
                    self.wfile.write(payload)
                except BrokenPipeError:
                    pass

            def log_message(self, _format, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / "model.ckpt"
            download(
                f"http://127.0.0.1:{server.server_port}/model.ckpt",
                target,
                expected_sha256=hashlib.sha256(payload).hexdigest(),
                attempts=2,
                timeout=0.01,
                retry_delay=0,
            )

            self.assertEqual(target.read_bytes(), payload)
            self.assertEqual(Handler.requests, 2)

    def test_retries_corrupt_or_truncated_successful_response(self):
        for truncated in (False, True, "chunked"):
            with self.subTest(truncated=truncated):
                self.check_integrity_retry(truncated=truncated, recover=True)

    def test_exhausted_integrity_retries_preserve_existing_target(self):
        self.check_integrity_retry(truncated=False, recover=False)

    def check_integrity_retry(self, *, truncated, recover):
        payload = b"verified model"

        class Handler(BaseHTTPRequestHandler):
            requests = 0

            def do_GET(self):
                type(self).requests += 1
                valid = recover and type(self).requests > 1
                body = payload if valid else b"corrupt"
                self.send_response(200)
                if truncated == "chunked" and not valid:
                    self.send_header("Transfer-Encoding", "chunked")
                    body = b"e\r\ncorrupt"
                else:
                    self.send_header("Content-Length", str(len(payload) if truncated else len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format, *_args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as root:
                target = Path(root) / "model.ckpt"
                target.write_bytes(b"previous verified model")

                def run_download():
                    download(
                        f"http://127.0.0.1:{server.server_port}/model.ckpt",
                        target,
                        expected_sha256=hashlib.sha256(payload).hexdigest(),
                        attempts=2,
                        retry_delay=0,
                    )

                if recover:
                    run_download()
                    self.assertEqual(target.read_bytes(), payload)
                else:
                    with self.assertRaisesRegex(OSError, "checksum mismatch"):
                        run_download()
                    self.assertEqual(target.read_bytes(), b"previous verified model")
                self.assertEqual(Handler.requests, 2)
                self.assertFalse(target.with_suffix(".ckpt.part").exists())
        finally:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
