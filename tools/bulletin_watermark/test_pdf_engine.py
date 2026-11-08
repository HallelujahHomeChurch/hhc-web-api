import io
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

import engine
import pdf_engine


class PDFContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.request = {'operation': 'render_pdf', 'source': 'source.pdf', 'output': 'marked.pdf', 'payload': '0'*61}

    def read(self):
        (self.root/'request.json').write_text(json.dumps(self.request))
        return pdf_engine.read_request(self.root)

    def test_platform_poppler_pins(self):
        self.assertEqual(pdf_engine.POPPLER_VERSIONS, {'macos-arm64': '26.05.0', 'linux-amd64': '25.03.0'})
        self.assertEqual(pdf_engine.POPPLER_VERSION, pdf_engine.POPPLER_VERSIONS.get(engine.runtime_target(), 'unsupported'))
        self.assertEqual(pdf_engine.PYPDF_VERSION, '6.21.4')
        self.assertTrue(pdf_engine.PDF_ENGINE_VERSION.endswith('-pypdf6.14.2'))

    def test_valid_render(self):
        self.assertEqual(self.read(), self.request)

    def test_render_scope_accepts_only_non_account_metadata(self):
        scope = {'version': 1, 'issueId': '00000000-0000-4000-8000-000000000001', 'series': 'general', 'locale': 'zh-Hant',
                 'revision': 7, 'sourceSha256': 'a'*64, 'rendererVersion': 'trustmark-q-bch5-pdf-1'}
        self.request['scope'] = scope
        self.assertEqual(self.read(), self.request)
        self.request['scope'] = {**scope, 'series': 'children', 'locale': 'en'}
        self.assertEqual(self.read(), self.request)
        for value in [{**scope, 'accountId': scope['issueId']}, {**scope, 'series': 'children', 'locale': 'zh-Hans'},
                      {**scope, 'version': 2}, {**scope, 'sourceSha256': 'A'*64}]:
            with self.subTest(value=value):
                self.request['scope'] = value
                with self.assertRaises(engine.JobError): self.read()

    def test_invalid_payload_paths_and_fields(self):
        for field, value in [('payload', '0'*60), ('source', '../source.pdf'), ('output', '/tmp/out.pdf'), ('model_url', 'https://example.org')]:
            with self.subTest(field=field):
                request = self.request.copy()
                self.request[field] = value
                with self.assertRaises(engine.JobError): self.read()
                self.request = request

    def test_template_contract(self):
        self.request = {'operation': 'templates', 'source': 'input.pdf', 'query': 'query.png',
                        'candidates': [{'id': 'a', 'payload': '0'*61}, {'id': 'b', 'payload': '1'*61}]}
        self.read()
        for field, value in [('query', '../query.png'), ('query', '/tmp/query.png'), ('candidates', [{'id': 'a', 'payload': '0'*61}]), ('candidates', [{'id': 'a', 'payload': '0'*61}]*2)]:
            with self.subTest(field=field):
                request = self.request.copy()
                self.request[field] = value
                with self.assertRaises(engine.JobError): self.read()
                self.request = request

    def test_query_pdf_contract(self):
        self.request = {'operation': 'query_pdf', 'source': 'input.pdf'}
        self.assertEqual(self.read(), self.request)

    def test_locate_and_prelocated_template_contracts(self):
        self.request = {'operation': 'locate', 'source': 'input.pdf', 'query': 'query.png'}
        self.assertEqual(self.read(), self.request)
        self.request = {'operation': 'templates', 'source': 'input.pdf', 'query': 'query.png', 'page': 2,
                        'candidates': [{'id': 'a', 'payload': '0'*61}, {'id': 'b', 'payload': '1'*61}]}
        self.assertEqual(self.read(), self.request)
        for page in (0, 1.5, 2**31):
            with self.subTest(page=page):
                self.request['page'] = page
                with self.assertRaises(engine.JobError): self.read()

    def test_scope_hint_accepts_only_the_versioned_non_account_allowlist(self):
        valid = {'version': 1, 'issueId': '00000000-0000-4000-8000-000000000001', 'series': 'general', 'locale': 'zh-Hant',
                 'revision': 7, 'sourceSha256': 'a'*64, 'rendererVersion': 'trustmark-q-bch5-pdf-1'}
        class Reader:
            metadata = {pdf_engine.PDF_SCOPE_METADATA_KEY: json.dumps(valid)}
        self.assertEqual(pdf_engine.scope_hint(Reader()), valid)
        for value in [None, '{', json.dumps({**valid, 'accountId': valid['issueId']}),
                      json.dumps({**valid, 'version': 2}), json.dumps({**valid, 'sourceSha256': 'A'*64})]:
            with self.subTest(value=value):
                Reader.metadata = {} if value is None else {pdf_engine.PDF_SCOPE_METADATA_KEY: value}
                self.assertIsNone(pdf_engine.scope_hint(Reader()))

    def test_publication_no_partial_outputs(self):
        stage = self.root/'stage'
        stage.mkdir()
        (stage/'a.png').write_bytes(b'a')
        (stage/'b.png').write_bytes(b'b')
        original_link = os.link
        def fail_second(src, dest):
            if Path(dest).name == 'b.png': raise OSError('full')
            original_link(src, dest)
        with patch('os.link', side_effect=fail_second):
            with self.assertRaises(engine.JobError): pdf_engine.publish_files(stage, self.root, ['a.png', 'b.png'])
        self.assertFalse((self.root/'a.png').exists())
        self.assertFalse((self.root/'b.png').exists())

    def test_pdf_write_preserves_existing_file(self):
        from unittest.mock import Mock
        destination = self.root/'existing.pdf'
        destination.write_bytes(b'existing')
        with self.assertRaises(engine.JobError): pdf_engine.write_pdf(Mock(), destination)
        self.assertEqual(destination.read_bytes(), b'existing')

    def test_publication_preserves_existing_file(self):
        stage = self.root/'stage'
        stage.mkdir()
        (stage/'a.png').write_bytes(b'new')
        (self.root/'a.png').write_bytes(b'old')
        with self.assertRaises(engine.JobError): pdf_engine.publish_files(stage, self.root, ['a.png'])
        self.assertEqual((self.root/'a.png').read_bytes(), b'old')



@unittest.skipUnless(os.environ.get('HHC_WATERMARK_PDF_TESTS') == '1', 'requires PDF runtime')
class PDFPreservationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.fixtures = Path(__file__).resolve().parents[2]/'internal/bulletinwatermark/testdata'
        self.executable = Path(os.environ['HHC_WATERMARK_PDFTOPPM'])

    def test_invalid_documents(self):
        for name in ('corrupt.pdf', 'truncated.pdf', 'encrypted.pdf', 'signed.pdf', 'too-many-pages.pdf'):
            with self.subTest(name=name):
                with self.assertRaises(engine.JobError): pdf_engine.read_pdf(self.fixtures/name)

    def test_query_pdf_renders_all_pages(self):
        source = self.root/'input.pdf'
        source.write_bytes((self.fixtures/'tagged.pdf').read_bytes())
        (self.root/'request.json').write_text(json.dumps({'operation': 'query_pdf', 'source': 'input.pdf'}))
        result = pdf_engine.run_job(self.root)
        self.assertEqual(result['status'], 'query_pdf')
        self.assertIsNone(result['scope_hint'])
        self.assertEqual(len(result['images']), len(pdf_engine.read_pdf(source)[0].pages))
        self.assertTrue(all((self.root/name).is_file() for name in result['images']))

    def test_locate_returns_only_the_unique_page_and_runtime_binding(self):
        source = self.root/'input.pdf'
        source.write_bytes((self.fixtures/'tagged.pdf').read_bytes())
        pdf_engine.raster_page(self.executable, source, 1, self.root).save(self.root/'query.png')
        (self.root/'request.json').write_text(json.dumps({'operation': 'locate', 'source': 'input.pdf', 'query': 'query.png'}))
        with patch.object(pdf_engine, 'check_pdf_runtime', return_value=self.executable), patch.object(engine, 'locate_page', return_value=0):
            result = pdf_engine.run_job(self.root)
        self.assertEqual(result, {'status': 'located', 'page': 1,
                                  'pdf_source_sha256': engine.sha256(source),
                                  'pdf_engine_version': pdf_engine.PDF_ENGINE_VERSION,
                                  'model_sha256': engine.MODEL_SHA256})

    def test_undefined_optional_reference_is_recovered(self):
        from pypdf import PdfWriter
        writer = PdfWriter()
        writer.add_blank_page(width=612, height=792)
        stream = io.BytesIO()
        writer.write(stream)
        data = stream.getvalue()
        info = re.search(rb'/Info ([0-9]) 0 R', data)
        self.assertIsNotNone(info)
        missing = next(value for value in b'987654321' if bytes([value]) + b' 0 obj' not in data)
        data = data[:info.start(1)] + bytes([missing]) + data[info.end(1):]
        source = self.root/'undefined-info.pdf'
        source.write_bytes(data)

        reader, _ = pdf_engine.read_pdf(source)

        self.assertEqual(len(reader.pages), 1)

    def test_raster_transport_is_uncompressed_and_bounded(self):
        import subprocess
        run = subprocess.run
        with patch.object(pdf_engine.subprocess, 'run', wraps=run) as process:
            image = pdf_engine.raster_page(self.executable, self.fixtures/'tagged.pdf', 1, self.root)
        self.assertEqual(image.width, pdf_engine.RASTER_WIDTH)
        self.assertNotIn('-png', process.call_args.args[0])
        with patch.object(engine, 'MAX_PIXELS', 1):
            with self.assertRaises(engine.JobError):
                pdf_engine.raster_page(self.executable, self.fixtures/'tagged.pdf', 1, self.root)
        self.assertFalse((self.root/'raster.ppm').exists())

    def test_rgba_arithmetic_and_zero_difference(self):
        import numpy as np
        from PIL import Image
        rng = np.random.default_rng(7)
        original = rng.integers(0, 256, (100, 100, 3), dtype='uint8')
        marked = np.clip(original.astype(int)+rng.integers(-12, 13, original.shape), 0, 255).astype('uint8')
        rgba = np.asarray(pdf_engine.rgba_residual(Image.fromarray(original), Image.fromarray(marked)))
        alpha = rgba[:, :, 3:4]/255
        reconstructed = np.rint(rgba[:, :, :3]*alpha+original*(1-alpha)).astype('uint8')
        self.assertTrue(np.array_equal(reconstructed, marked))
        unchanged = np.asarray(pdf_engine.rgba_residual(Image.fromarray(original), Image.fromarray(original)))
        self.assertFalse(unchanged[:, :, 3].any())

    def test_background_mark_does_not_touch_text_edges(self):
        import numpy as np
        from PIL import Image
        pixels = np.full((80, 120, 3), 255, np.uint8)
        pixels[30:50, 40:80] = 0
        original = Image.fromarray(pixels)
        class Encoder:
            def encode(self, image, payload, **kwargs):
                return Image.fromarray(np.maximum(np.asarray(image).astype(int)-5, 0).astype('uint8'))
        marked = np.asarray(pdf_engine.background_mark(Encoder(), original, '0'*61))
        self.assertTrue(np.all(marked[23:57, 33:87] == pixels[23:57, 33:87]))
        self.assertTrue(np.all(marked[:20] == 250))

    def test_template_hashes_match_issued_pdf_with_userunit(self):
        import shutil
        import numpy as np
        from PIL import Image
        from pypdf import PdfReader, PdfWriter
        from pypdf.generic import NameObject, NumberObject
        writer = PdfWriter()
        writer.clone_document_from_reader(PdfReader(self.fixtures/'tagged.pdf'))
        writer.pages[0][NameObject('/UserUnit')] = NumberObject(2)
        source = self.root/'source.pdf'
        pdf_engine.write_pdf(writer, source)
        class FakeEncoder:
            def encode(self, original, payload, **kwargs):
                pixels = np.asarray(original).astype(int)
                pixels[40:90, 70:150] -= 7 if payload == '0'*61 else 11
                return Image.fromarray(np.clip(pixels, 0, 255).astype('uint8'))
        results = []
        scope = {'version': 1, 'issueId': '00000000-0000-4000-8000-000000000001', 'series': 'general', 'locale': 'zh-Hant',
                 'revision': 7, 'sourceSha256': 'a'*64, 'rendererVersion': pdf_engine.PDF_ENGINE_VERSION}
        for operation in ('render_pdf', 'templates'):
            job = self.root/operation
            job.mkdir(mode=0o700)
            shutil.copyfile(source, job/'input.pdf')
            request = {'operation': operation, 'source': 'input.pdf'}
            if operation == 'render_pdf': request.update(output='marked.pdf', payload='0'*61, scope=scope)
            else:
                pdf_engine.raster_page(self.executable, self.root/'render_pdf/marked.pdf', 1, self.root).save(job/'query.png')
                request.update(query='query.png', candidates=[{'id': 'a', 'payload': '0'*61}, {'id': 'b', 'payload': '1'*61}])
            (job/'request.json').write_text(json.dumps(request))
            with patch.object(pdf_engine, 'check_pdf_runtime', return_value=self.executable), patch.object(engine, 'load_encoder', return_value=FakeEncoder()), patch.object(engine, 'locate_page', return_value=0):
                results.append(pdf_engine.run_job(job))
            self.assertFalse(list(job.glob('.pdf-watermark-*')))
        issued, templates = results
        self.assertEqual(json.loads(PdfReader(self.root/'render_pdf/marked.pdf').metadata[pdf_engine.PDF_SCOPE_METADATA_KEY]), scope)
        self.assertNotIn('accountId', PdfReader(self.root/'render_pdf/marked.pdf').metadata[pdf_engine.PDF_SCOPE_METADATA_KEY])
        self.assertEqual(issued['pages'][0]['source_sha256'], templates['source_sha256'])
        self.assertEqual(issued['pages'][0]['output_sha256'], templates['candidates'][0]['output_sha256'])
        self.assertNotEqual(templates['candidates'][0]['output_sha256'], templates['candidates'][1]['output_sha256'])
        self.assertEqual(PdfReader(self.root/'render_pdf/marked.pdf').pages[0]['/UserUnit'], 2)
        for candidate in templates['candidates']:
            self.assertEqual(candidate['output_sha256'], engine.sha256(self.root/'templates'/candidate['image']))
        self.assertEqual(templates['source_sha256'], engine.sha256(self.root/'templates/source.png'))

    @unittest.skipUnless(engine.runtime_target() == 'linux-amd64', 'frozen Linux page hashes')
    def test_historical_pypdf_page_hashes(self):
        import shutil
        import numpy as np
        from PIL import Image
        baseline = json.loads((Path(__file__).parent/'testdata/pypdf-6.14.2-pages.json').read_text())
        self.assertEqual(baseline['pypdf_version'], '6.14.2')
        self.assertEqual(baseline['renderer_version'], pdf_engine.PDF_ENGINE_VERSION)
        class Encoder:
            def encode(self, original, payload, **kwargs):
                pixels = np.asarray(original).astype(int)
                pixels[40:90, 70:150] -= 7 if payload == '0'*61 else 11
                return Image.fromarray(np.clip(pixels, 0, 255).astype('uint8'))
        for name, pages in baseline['fixtures'].items():
            self.assertEqual(len(pdf_engine.read_pdf(self.fixtures/name)[0].pages), len(pages))
            for number, expected in enumerate(pages, 1):
                with self.subTest(fixture=name, page=number):
                    job = self.root/f'{name}-{number}'
                    job.mkdir(mode=0o700)
                    shutil.copyfile(self.fixtures/name, job/'input.pdf')
                    pdf_engine.raster_page(self.executable, job/'input.pdf', number, job).save(job/'query.png')
                    request = {'operation': 'templates', 'source': 'input.pdf', 'query': 'query.png', 'page': number,
                               'candidates': [{'id': 'a', 'payload': '0'*61}, {'id': 'b', 'payload': '1'*61}]}
                    (job/'request.json').write_text(json.dumps(request))
                    with patch.object(engine, 'load_encoder', return_value=Encoder()):
                        result = pdf_engine.run_job(job)
                    self.assertEqual(result['source_sha256'], expected['source_sha256'])
                    self.assertEqual([v['output_sha256'] for v in result['candidates']], expected['output_sha256'])
                    self.assertEqual((result['width'], result['height']), (expected['width'], expected['height']))
                    self.assertEqual(result['pdf_engine_version'], pdf_engine.PDF_ENGINE_VERSION)

    def test_pdf_output_limit_removes_file(self):
        from pypdf import PdfReader, PdfWriter
        writer = PdfWriter()
        writer.clone_document_from_reader(PdfReader(self.fixtures/'tagged.pdf'))
        destination = self.root/'too-big.pdf'
        with patch.object(pdf_engine, 'MAX_OUTPUT_BYTES', 10):
            with self.assertRaisesRegex(engine.JobError, 'pdf_output_size_limit'):
                pdf_engine.write_pdf(writer, destination)
        self.assertFalse(destination.exists())

    def test_matrix_and_tagged_structure_preserved(self):
        import numpy as np
        from PIL import Image
        from pypdf import PdfReader, PdfWriter
        for name in ('matrix.pdf', 'tagged.pdf'):
            with self.subTest(name=name):
                reader, _ = pdf_engine.read_pdf(self.fixtures/name)
                writer = PdfWriter()
                writer.clone_document_from_reader(reader)
                expected = []
                for number, page in enumerate(writer.pages, 1):
                    original = pdf_engine.raster_page(self.executable, self.fixtures/name, number, self.root)
                    self.assertEqual(original.width, pdf_engine.RASTER_WIDTH)
                    pixels = np.asarray(original).astype(int)
                    # Localized, unambiguous test residual catches rotation/crop mapping.
                    pixels[40:90, 70:150] = np.maximum(0, pixels[40:90, 70:150]-9)
                    marked = Image.fromarray(pixels.astype('uint8'))
                    expected.append(np.asarray(marked))
                    pdf_engine.overlay_page(page, original, marked)
                    form = next(ref.get_object() for key, ref in page['/Resources']['/XObject'].items() if str(key).startswith('/HHCWatermark'))
                    for ref in form['/Resources']['/XObject'].values():
                        image = ref.get_object()
                        self.assertNotIn('/ASCII85Decode', str(image.get('/Filter')))
                        if '/SMask' in image: self.assertNotIn('/ASCII85Decode', str(image['/SMask'].get('/Filter')))
                output = self.root/name
                pdf_engine.write_pdf(writer, output)
                reopened = PdfReader(output)
                self.assertGreaterEqual(reopened.pdf_header, '%PDF-1.4')
                self.assertEqual(len(reopened.pages), len(reader.pages))
                for number, (before, after) in enumerate(zip(reader.pages, reopened.pages), 1):
                    self.assertEqual(before.extract_text(), after.extract_text())
                    from pypdf.generic import ArrayObject
                    original_contents = before.get('/Contents').get_object()
                    original_streams = list(original_contents) if isinstance(original_contents, ArrayObject) else [original_contents]
                    new_streams = after['/Contents'][1:-1]
                    self.assertEqual([x.get_object().get_data() for x in original_streams], [x.get_object().get_data() for x in new_streams])
                    self.assertEqual(list(before.mediabox), list(after.mediabox))
                    self.assertEqual(list(before.cropbox), list(after.cropbox))
                    self.assertEqual(before.rotation, after.rotation)
                    self.assertEqual(before.get('/UserUnit', 1), after.get('/UserUnit', 1))
                    links = lambda p: [(list(a.get_object()['/Rect']), str(a.get_object().get('/A'))) for a in p.get('/Annots', [])]
                    self.assertEqual(links(before), links(after))
                    self.assertIn(b'/Artifact BMC', after.get_contents().get_data())
                    rendered = np.asarray(pdf_engine.raster_page(self.executable, output, number, self.root)).astype(float)
                    reference = expected[number-1].astype(float)
                    self.assertEqual(rendered.shape, reference.shape)
                    self.assertLess(np.mean((rendered-reference)**2), .1)
                if name == 'tagged.pdf':
                    root = reopened.trailer['/Root']
                    self.assertTrue(root['/MarkInfo']['/Marked'])
                    self.assertEqual(root['/StructTreeRoot']['/K'][0].get_object()['/K'], 0)
                    self.assertEqual(root['/StructTreeRoot']['/ParentTree']['/Nums'][0], 0)
                    self.assertEqual(reopened.pages[0]['/StructParents'], 0)


if __name__ == '__main__': unittest.main()
