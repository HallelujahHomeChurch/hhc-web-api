import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import engine


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.request = dict(operation='encode', source='source.png', payload='0'*61, output='marked.png')

    def read(self):
        (self.root/'request.json').write_text(json.dumps(self.request))
        return engine.read_request(self.root)

    def test_valid_encode(self):
        self.assertEqual(self.read(), self.request)

    def test_invalid_payloads(self):
        for value in ['0'*60, '0'*62, 'x'*61, 0, None]:
            with self.subTest(value=type(value).__name__):
                self.request['payload'] = value
                with self.assertRaises(engine.JobError): self.read()

    def test_invalid_paths(self):
        for name in ['/tmp/a', '../a', 'a/../b', 'https://example.org/a', 'a\\b', '', '.']:
            with self.subTest(name=name):
                with self.assertRaises(engine.JobError): engine.resolve_job_file(self.root, name)

    def test_symlink_rejected_even_inside(self):
        (self.root/'a').write_text('x')
        (self.root/'link').symlink_to(self.root/'a')
        with self.assertRaises(engine.JobError): engine.resolve_job_file(self.root, 'link')

    def test_unknown_fields_and_operation(self):
        self.request['model_url'] = 'https://example.org/model'
        with self.assertRaises(engine.JobError): self.read()
        self.request.pop('model_url')
        self.request['operation'] = 'other'
        with self.assertRaises(engine.JobError): self.read()

    def test_candidate_contract(self):
        c = dict(id='a', image='a.png', source_sha256='a'*64, output_sha256='b'*64,
                 engine_version=engine.ENGINE_VERSION, model_sha256=engine.MODEL_SHA256)
        self.request = dict(operation='detect', source='source.png', query='query.png', candidates=[c, dict(c, id='b')])
        self.read()
        for candidates in [[c], [c, c], [dict(c, output_sha256='bad'), dict(c, id='b')], [c]*65]:
            self.request['candidates'] = candidates
            with self.assertRaises(engine.JobError): self.read()

    def test_cli_no_payload_echo(self):
        self.request['payload'] = 'sensitive-invalid'
        self.read_file_only()
        result = subprocess.run([sys.executable, str(Path(engine.__file__)), '--job-dir', str(self.root)], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)['status'], 'failed')
        self.assertNotIn('sensitive-invalid', result.stdout + result.stderr)

    def read_file_only(self):
        (self.root/'request.json').write_text(json.dumps(self.request))



class FailureTests(unittest.TestCase):
    setUp = ContractTests.setUp
    def test_invalid_candidate_metadata_fails_before_matching(self):
        from types import SimpleNamespace
        from unittest.mock import Mock, patch
        original = SimpleNamespace(width=100, height=100, size=(100, 100))
        good = dict(id='a', image='a.png', source_sha256='a'*64,
                    output_sha256='b'*64, engine_version=engine.ENGINE_VERSION,
                    model_sha256=engine.MODEL_SHA256)
        cases = [
            ('candidate_source_mismatch', dict(good, source_sha256='c'*64), original),
            ('candidate_hash_mismatch', dict(good, output_sha256='c'*64), original),
            ('candidate_dimension_mismatch', good, SimpleNamespace(size=(50, 50))),
            ('invalid_image', good, None),
        ]
        for error, candidate, image in cases:
            with self.subTest(error=error):
                request = dict(operation='detect', source='source.png', query='query.png',
                               candidates=[candidate, dict(good, id='b', image='b.png')])
                def read_image(path):
                    if path.name in ('source.png', 'query.png'): return original
                    if image is None: raise engine.JobError('invalid_image')
                    return image
                with patch.dict('sys.modules', {'cv2': Mock(), 'numpy': Mock()}), patch.object(engine, 'check_runtime'), patch.object(engine, 'read_image', side_effect=read_image), patch.object(engine, 'sha256', side_effect=lambda path: 'a'*64 if path.name == 'source.png' else 'b'*64), patch.object(engine, 'identify_ecc') as identify:
                    with self.assertRaisesRegex(engine.JobError, error):
                        engine.detect(self.root, request)
                    identify.assert_not_called()

    def test_candidate_version_mismatch(self):
        good = dict(id='a', image='a.png', source_sha256='a'*64,
                    output_sha256='b'*64, engine_version=engine.ENGINE_VERSION,
                    model_sha256=engine.MODEL_SHA256)
        for field, value in [('engine_version', 'another-engine'), ('model_sha256', '0'*64)]:
            with self.subTest(field=field):
                request = dict(operation='detect', source='source.png', query='query.png',
                               candidates=[dict(good, **{field: value}), dict(good, id='b')])
                (self.root/'request.json').write_text(json.dumps(request))
                with self.assertRaisesRegex(engine.JobError, 'candidate_version_mismatch'):
                    engine.read_request(self.root)

    def test_private_directory_required(self):
        self.root.chmod(0o755)
        with self.assertRaisesRegex(engine.JobError, 'job_directory_not_private'):
            engine.run_job(self.root)

    def test_existing_output_preserved(self):
        (self.root/'marked.png').write_bytes(b'existing')
        with self.assertRaisesRegex(engine.JobError, 'output_exists'):
            engine.encode(self.root, self.request)
        self.assertEqual((self.root/'marked.png').read_bytes(), b'existing')

    def test_oversized_output_is_not_published(self):
        from unittest.mock import Mock, patch
        model = Mock()
        model.encode.return_value.save.side_effect = lambda stream, **kwargs: stream.write(b'12345')
        with patch.object(engine, 'MAX_BYTES', 4), patch.object(engine, 'check_runtime'), patch.object(engine, 'read_image'), patch.object(engine, 'sha256', return_value='a'*64), patch.object(engine, 'load_encoder', return_value=model):
            with self.assertRaisesRegex(engine.JobError, 'output_size_limit'):
                engine.encode(self.root, self.request)
        self.assertFalse((self.root/'marked.png').exists())
        self.assertEqual(list(self.root.glob('.watermark-*')), [])

    def test_failed_write_removes_temporary_output(self):
        from unittest.mock import Mock, patch
        model = Mock()
        model.encode.return_value.save.side_effect = OSError('disk full')
        with patch.object(engine, 'check_runtime'), patch.object(engine, 'read_image'), patch.object(engine, 'sha256', return_value='a'*64), patch.object(engine, 'load_encoder', return_value=model):
            with self.assertRaisesRegex(engine.JobError, 'output_write_failed'):
                engine.encode(self.root, self.request)
        self.assertFalse((self.root/'marked.png').exists())
        self.assertEqual(list(self.root.glob('.watermark-*')), [])

    def test_image_boundaries_without_optional_dependencies(self):
        from types import SimpleNamespace
        from unittest.mock import MagicMock, patch
        image = MagicMock(format='PNG', width=5000, height=5000)
        image.__enter__.return_value = image
        api = SimpleNamespace(open=MagicMock(return_value=image), DecompressionBombError=RuntimeError)
        source = self.root/'source.png'
        source.write_bytes(b'test')
        with patch.dict('sys.modules', {'PIL': SimpleNamespace(Image=api)}):
            with self.assertRaisesRegex(engine.JobError, 'image_size_limit'):
                engine.read_image(self.root/'missing-candidate.png')
            with self.assertRaisesRegex(engine.JobError, 'image_pixel_limit'):
                engine.read_image(source)
            image.format = 'GIF'
            with self.assertRaisesRegex(engine.JobError, 'unsupported_image'):
                engine.read_image(source)
            with source.open('wb') as handle: handle.truncate(engine.MAX_BYTES+1)
            with self.assertRaisesRegex(engine.JobError, 'image_size_limit'):
                engine.read_image(source)

    def test_model_missing_or_writable_rejected(self):
        from unittest.mock import patch
        with patch.dict('os.environ', {}, clear=True):
            with self.assertRaisesRegex(engine.JobError, 'model_unavailable'): engine.load_encoder()
        with patch.dict('os.environ', {'HHC_WATERMARK_MODEL_DIR': str(self.root)}):
            with self.assertRaisesRegex(engine.JobError, 'model_not_readonly'): engine.load_encoder()

    def test_model_mismatch_precedes_import(self):
        from unittest.mock import patch
        model = self.root/'models'
        model.mkdir()
        for name in engine.MODEL_FILES:
            path = model/name
            path.write_bytes(b'bad model')
            path.chmod(0o444)
        model.chmod(0o555)
        self.addCleanup(model.chmod, 0o700)
        with patch.dict('os.environ', {'HHC_WATERMARK_MODEL_DIR': str(model)}):
            with self.assertRaisesRegex(engine.JobError, 'model_hash_mismatch'): engine.load_encoder()

    def test_duplicate_json_key(self):
        (self.root/'request.json').write_text('{"operation":"encode","operation":"detect"}')
        with self.assertRaises(engine.JobError): engine.read_request(self.root)

    def test_page_selection_requires_a_clear_unique_match(self):
        self.assertEqual(engine.choose_unique_page([(1, 34, .81), (2, 12, .55), (3, 0, 0)]), 1)
        for scores in [[], [(1, 9, .9)], [(1, 30, .8), (2, 25, .75)], [(1, 40, .4), (2, 4, .2)]]:
            with self.subTest(scores=scores):
                self.assertIsNone(engine.choose_unique_page(scores))

    def test_evaluate_reports_every_failed_case(self):
        cases = []
        for i in range(2):
            folder = self.root/f'job{i}'
            folder.mkdir(mode=0o700)
            (folder/'request.json').write_text('{}')
            cases.append(dict(id=f'case{i}', job_dir=folder.name, expected_candidate_id=None))
        manifest = self.root/'evaluation.json'
        manifest.write_text(json.dumps({'cases': cases}))
        result = engine.evaluate(manifest)
        self.assertEqual(len(result['cases']), 2)
        self.assertTrue(all(c['status'] == 'failed' and not c['correct'] and not c['false_attribution'] for c in result['cases']))


import os


@unittest.skipUnless(os.environ.get('HHC_WATERMARK_INTEGRATION') == '1', 'requires pinned offline model runtime')
class IntegrationTests(unittest.TestCase):
    def setUp(self):
        from PIL import Image, ImageDraw
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = Image.new('RGB', (512, 640), 'white')
        draw = ImageDraw.Draw(self.source)
        import random
        rng = random.Random(71)
        for i in range(180):
            x, y = rng.randrange(490), rng.randrange(620)
            draw.text((x, y), str(i), fill=(20, 30, 40))
        self.source.save(self.root/'source.png')

    def cli(self, folder, request):
        (folder/'request.json').write_text(json.dumps(request))
        process = subprocess.run([sys.executable, str(Path(engine.__file__)), '--job-dir', str(folder)], capture_output=True, text=True, timeout=300)
        result = json.loads(process.stdout)
        self.assertNotIn('payload', process.stdout)
        return result

    def test_cold_reconstruction_and_detection_fail_closed(self):
        import shutil
        candidates = []
        hashes = []
        for i, payload in enumerate(['0'*61, '0'*61, '1'*61, '01'*30+'0']):
            folder = self.root/f'encode{i}'
            folder.mkdir(mode=0o700)
            shutil.copyfile(self.root/'source.png', folder/'source.png')
            result = self.cli(folder, dict(operation='encode', source='source.png', output='marked.png', payload=payload))
            self.assertEqual(result['status'], 'encoded', result)
            hashes.append(result['output_sha256'])
            shutil.copyfile(folder/'marked.png', self.root/f'candidate{i}.png')
            candidates.append(dict(id=f'c{i}', image=f'candidate{i}.png', **{k: v for k, v in result.items() if k != 'status'}))
        self.assertEqual(hashes[0], hashes[1])
        self.assertNotEqual(hashes[0], hashes[2])
        for query, selected in [('source.png', [candidates[0], candidates[2]]),
                                ('candidate3.png', [candidates[0], candidates[2]]),
                                ('candidate0.png', [candidates[2], candidates[3]])]:
            result = self.cli(self.root, dict(operation='detect', source='source.png', query=query, candidates=selected))
            self.assertEqual(result, {'status': 'inconclusive', 'detector_version': engine.DETECTOR_VERSION})
        result = self.cli(self.root, dict(operation='detect', source='source.png', query='candidate0.png', candidates=[candidates[0], candidates[2]]))
        self.assertEqual(result, {'status': 'matched', 'candidate_id': 'c0', 'detector_version': engine.DETECTOR_VERSION})
        (self.root/'corrupt.png').write_bytes(b'not an image')
        result = self.cli(self.root, dict(operation='detect', source='source.png', query='corrupt.png', candidates=[candidates[0], candidates[2]]))
        self.assertEqual(result['status'], 'failed')


class RuntimeTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('HHC_WATERMARK_INTEGRATION') == '1', 'locked runtime required')
    def test_lightning_fixed_checkpoint_guards(self):
        import builtins
        import importlib.metadata
        from unittest.mock import patch
        import lightning.pytorch as pl
        import lightning.pytorch.core.saving as saving

        self.assertEqual(importlib.metadata.version('lightning'), '2.6.6')
        pin = 'lightning==2.6.6 --hash=sha256:2bcbd6ee840071cd076c7dc9953d7ac1040df92f1c5fa76583aae644f037ab85'
        self.assertIn(pin, Path(__file__).with_name('requirements-linux.lock.txt').read_text().splitlines())

        class Probe(pl.LightningModule):
            pass

        # Exercise the real checkpoint loader; these inputs must fail before importing targets.
        cases = [
            ({'_instantiator': 'hhc_checkpoint_guard.never_call'}, 'not in the allowlist'),
            ({'_instantiator': 42}, 'not in the allowlist'),
            ({'_instantiator': 'lightning.pytorch.cli.instantiate_module',
              '_class_path': 'hhc_checkpoint_guard.NeverImport'}, 'already imported'),
        ]
        for params, message in cases:
            with self.subTest(params=params), patch.object(builtins, '__import__', wraps=builtins.__import__) as imports:
                with self.assertRaisesRegex(ValueError, message):
                    saving._load_state(Probe, {'state_dict': {}, 'hyper_parameters': params}, strict=False)
                self.assertFalse(any(call.args[0].startswith(('hhc_checkpoint_guard', 'lightning.pytorch.cli'))
                                     for call in imports.call_args_list))
        self.assertIsInstance(saving._load_state(Probe, {'state_dict': {}, 'hyper_parameters': {}}, strict=False), Probe)

    def test_pinned_linux_cpu_runtime(self):
        from unittest.mock import patch
        versions = dict(engine.RUNTIME_PACKAGES, torch='2.14.0+cpu', torchvision='0.29.0+cpu')
        with patch('platform.python_implementation', return_value='CPython'), patch('platform.python_version', return_value='3.12.14'), patch('platform.system', return_value='Linux'), patch('platform.machine', return_value='x86_64'), patch('importlib.metadata.version', side_effect=versions.__getitem__):
            engine.check_runtime()

    def test_supported_runtime_uses_locked_distributions(self):
        import importlib.metadata
        from unittest.mock import patch
        pins = dict(line.split()[0].lower().split('==', 1) for line in Path(__file__).with_name('requirements.lock.txt').read_text().splitlines() if '==' in line and not line.startswith('#'))
        with patch('platform.python_implementation', return_value='CPython'), patch('platform.python_version', return_value='3.12.14'), patch('platform.system', return_value='Darwin'), patch('platform.machine', return_value='arm64'), patch('importlib.metadata.version', side_effect=lambda name: pins[name.lower()]):
            engine.check_runtime()

    def test_unsupported_runtime_rejected(self):
        from unittest.mock import patch
        with patch('platform.machine', return_value='unsupported'):
            with self.assertRaisesRegex(engine.JobError, 'runtime_mismatch'):
                engine.check_runtime()


class BackgroundDetectorTests(unittest.TestCase):
    def setUp(self):
        try:
            import cv2
            import numpy as np
        except ImportError:
            self.skipTest('locked image runtime required')
        engine.cv2, engine.np = cv2, np
        self.np = np
        self.matrix = np.array([[1, 0, 0], [0, 1, 0]], dtype=np.float32)

    def test_mask_rejects_invalid_geometry_and_insufficient_background(self):
        np = self.np
        image = np.full((200, 200, 3), 255, np.uint8)
        for matrix in [self.matrix * np.nan, self.matrix * 0, self.matrix * .01,
                       np.array([[-1, 0, 0], [0, 1, 0]], dtype=np.float32)]:
            with self.subTest(matrix=matrix.tolist()):
                self.assertIsNone(engine.background_mask(image, matrix, (200, 200)))
        self.assertIsNone(engine.background_mask(image[:20, :20], self.matrix, (20, 20)))
        image[:] = 249
        self.assertIsNone(engine.background_mask(image, self.matrix, (200, 200)))
        image[50:80, 40:160] = 255  # More than 1000 pixels, but less than 5% after erosion.
        self.assertIsNone(engine.background_mask(image, self.matrix, (200, 200)))

    def test_mask_excludes_source_text_edges(self):
        np = self.np
        image = np.full((200, 200, 3), 250, np.uint8)
        image[80:120, 80:120] = 0
        mask = engine.background_mask(image, self.matrix, (200, 200))
        self.assertIsNotNone(mask)
        self.assertTrue(mask[20, 20])
        self.assertFalse(mask[75, 100])
        self.assertTrue(mask[72, 100])

    def test_masked_error_keeps_global_brightness_fit(self):
        np = self.np
        prediction = np.arange(1200, dtype=np.float32).reshape(20, 20, 3) % 256
        observed = prediction * .95 + 2
        mask = np.zeros((20, 20), bool)
        mask[:3] = True
        _, gain, offset = engine.affine_fit(prediction, observed)
        error, masked_gain, masked_offset = engine.affine_fit(prediction, observed, mask)
        self.assertEqual((gain, offset), (masked_gain, masked_offset))
        expected = np.mean((np.clip(prediction[mask].astype(float)*gain+offset, 0, 255)-observed[mask])**2)
        self.assertAlmostEqual(error, expected)

    def test_original_image_is_not_attributed(self):
        from PIL import Image
        from unittest.mock import patch
        np = self.np
        pixels = np.full((200, 200, 3), 255, np.uint8)
        pixels[80:120, 80:120] = 0
        original = Image.fromarray(pixels)
        candidates = [Image.fromarray(np.clip(pixels.astype(int)-n, 0, 255).astype(np.uint8)) for n in (5, 10)]
        with patch.object(engine, 'locate', return_value=self.matrix), patch.object(engine.cv2, 'findTransformECC', return_value=(1, self.matrix)):
            self.assertIsNone(engine.identify_ecc(original, original, candidates))

    def test_correlation_requires_frozen_score_and_runner_up_gap(self):
        self.assertIsNone(engine.choose_correlation([.89, .10]))
        self.assertIsNone(engine.choose_correlation([.95, .82]))
        self.assertEqual(engine.choose_correlation([.95, .70]), 0)

    def test_independent_detector_results_must_not_conflict(self):
        self.assertEqual(engine.combine_matches(4, None), 4)
        self.assertEqual(engine.combine_matches(None, 4), 4)
        self.assertEqual(engine.combine_matches(4, 4), 4)
        self.assertIsNone(engine.combine_matches(4, 21))
        self.assertIsNone(engine.combine_matches(None, None))

    def test_page_locator_accepts_a_unique_crop_and_rotation(self):
        import random
        from PIL import Image, ImageDraw
        pages = []
        for seed in range(3):
            randomizer = random.Random(seed)
            image = Image.new('RGB', (600, 800), 'white')
            draw = ImageDraw.Draw(image)
            for _ in range(120):
                x, y = randomizer.randrange(20, 560), randomizer.randrange(20, 760)
                width, height = randomizer.randrange(8, 70), randomizer.randrange(8, 70)
                color = tuple(randomizer.randrange(150) for _ in range(3))
                draw.rectangle((x, y, min(580, x + width), min(780, y + height)), outline=color, width=2)
            pages.append(image)
        query = pages[1].crop((40, 120, 560, 700)).rotate(7, expand=True, fillcolor='white')
        self.assertEqual(engine.locate_page(query, pages), 1)

    def test_page_locator_rejects_ambiguous_pages(self):
        from PIL import Image, ImageDraw
        page = Image.new('RGB', (600, 800), 'white')
        draw = ImageDraw.Draw(page)
        for row in range(20):
            draw.text((30 + row * 13, 30 + row * 35), f'same-content-{row}', fill='black')
        query = page.crop((20, 100, 580, 720))
        self.assertIsNone(engine.locate_page(query, [page, page.copy()]))


if __name__ == '__main__': unittest.main()
