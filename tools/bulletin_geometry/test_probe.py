import unittest
import xml.etree.ElementTree as ET
from unittest.mock import patch
from pathlib import Path

from probe import ProbeError, probe, verify_xml, verify_pdfinfo


def sample(issue=1733):
    pages = []
    for number in range(1, {1731: 20, 1733: 18, 1739: 12, 1740: 16}[issue] + 1):
        size = 'width="595" height="841"' if issue in (1739, 1740) or (issue == 1731 and number == 20) else 'width="612" height="792"'
        pages.append(f'<page number="{number}" {size}><fontspec id="0" size="12" family="Synthetic"/>'
                     '<text top="10" left="10" width="24" height="12" font="0">測試<b>文字</b></text></page>')
    return ('<pdf2xml>' + ''.join(pages) + '</pdf2xml>').encode()


class GeometryTests(unittest.TestCase):
    def test_annotation_and_compressed_object_features_are_rejected(self):
        from fixtures import verify_pdf_features
        for feature in (b'/Annots', b'/An#6Eots', b'/Type /Annot /Subtype /Text /Contents (PRIVATE_TEST_SENTINEL)',
                        b'/Type /ObjStm', b'/OpenAction', b'/URI'):
            with self.subTest(feature=feature), self.assertRaisesRegex(ProbeError, '^fixture_unsupported_pdf_feature$'):
                verify_pdf_features(b'%PDF-1.3\n<< ' + feature + b' >>')

    def test_font_size_is_recorded_across_page_definitions(self):
        from fixtures import font_definitions
        root = ET.fromstring('<pdf2xml><page><fontspec id="0" size="12" family="Synthetic" color="#000000"/></page>'
                             '<page><fontspec id="1" size="14" family="Synthetic" color="#000000"/></page></pdf2xml>')
        self.assertEqual(font_definitions(root), [
            {'id': '0', 'size': 12.0, 'family': 'Synthetic', 'color': '#000000'},
            {'id': '1', 'size': 14.0, 'family': 'Synthetic', 'color': '#000000'},
        ])

    def test_image_slots_record_page_identity(self):
        from fixtures import image_slots
        root = ET.fromstring('<pdf2xml><page number="11"><image top="1" left="2" width="3" height="4"/></page>'
                             '<page number="12"><image top="1" left="2" width="3" height="4"/></page></pdf2xml>')
        self.assertEqual(image_slots(root), [
            {'page': 11, 'top': '1', 'left': '2', 'width': '3', 'height': '4'},
            {'page': 12, 'top': '1', 'left': '2', 'width': '3', 'height': '4'},
        ])

    def test_image_payload_cannot_hide_an_original_image_or_qr(self):
        from fixtures import verify_pixels
        pixels = b'P6\n16 16\n255\n' + bytes((128, 160, 192)) * 256
        verify_pixels(pixels)
        verify_pixels(b'P6\n16 16\n255\n' + bytes((248, 248, 248)) * 256)
        with self.assertRaisesRegex(ProbeError, '^fixture_unknown_image$'):
            verify_pixels(pixels[:-1] + b'\x00')

    def test_fixture_metadata_cannot_retain_original_subject(self):
        from fixtures import verify_metadata
        with self.assertRaisesRegex(ProbeError, '^fixture_metadata_not_allowlisted$'):
            verify_metadata(b'Title: HHC sanitized geometry fixture\nAuthor: HHC synthetic fixture\nSubject: private\n')

    def test_allowlist_handles_poppler_line_merging_without_allowing_real_text(self):
        from fixtures import fixture_tokens
        self.assertEqual(fixture_tokens('TEXT測 COVER_WORSHIPTEXT測'), ['TEXT測', 'COVER_WORSHIP', 'TEXT測'])
        with self.assertRaisesRegex(ProbeError, '^fixture_text_not_allowlisted$'):
            fixture_tokens('TEXT測 真實姓名')

    def test_fixture_rejects_real_text_and_checks_component_start_pages(self):
        from fixtures import fixture_geometry
        xml = sample(1739)
        with self.assertRaisesRegex(ProbeError, '^fixture_text_not_allowlisted$'):
            fixture_geometry(xml, 1739)
        synthetic = xml.replace('測試<b>文字</b>'.encode(), 'TEXT測'.encode())
        with self.assertRaisesRegex(ProbeError, '^fixture_components_missing$'):
            fixture_geometry(synthetic, 1739)

    def test_lyrics_read_left_column_before_right_column(self):
        from fixtures import reading_order
        page = ET.fromstring('<page width="595"><text left="320" top="10">R1</text>'
                             '<text left="40" top="20">L2</text><text left="40" top="10">L1</text></page>')
        self.assertEqual([''.join(x.itertext()) for x in reading_order(page, True)], ['L1', 'L2', 'R1'])

    def test_supported_page_geometry(self):
        for issue in (1731, 1733, 1739, 1740):
            self.assertTrue(all(verify_xml(sample(issue), issue).values()))

    def test_poppler_font_definitions_are_document_scoped(self):
        xml = sample().replace(b'<fontspec id="0" size="12" family="Synthetic"/>', b'', 18)
        xml = xml.replace(b'<text ', b'<fontspec id="0" size="12" family="Synthetic"/><text ', 1)
        self.assertTrue(verify_xml(xml, 1733)['text_geometry'])

    def test_precise_source_boxes_not_just_truncated_xml(self):
        info = ''.join(f'Page {i} size: 595.32 x 841.92 pts (A4)\n' for i in range(1, 13)).encode()
        verify_pdfinfo(info, 1739)
        with self.assertRaisesRegex(ProbeError, '^page_box_mismatch$'):
            verify_pdfinfo(info.replace(b'595.32', b'595.50'), 1739)
        with self.assertRaisesRegex(ProbeError, '^page_count_mismatch$'):
            verify_pdfinfo(b'', 1739)

    def test_rejects_invalid_geometry_and_text(self):
        cases = [
            (sample(1731).replace(b'width="595"', b'width="612"'), 1731, 'page_box_mismatch'),
            (sample().replace(b'width="24"', b'width="0"'), 1733, 'invalid_geometry'),
            (sample().replace(b'top="10"', b'top="NaN"'), 1733, 'invalid_geometry'),
            (sample().replace(b'font="0"', b'font="1"'), 1733, 'missing_font'),
            (sample().replace('測試'.encode(), '\ufffd'.encode()), 1733, 'undecodable_text'),
            (sample().replace('測試'.encode(), b'aa').replace('文字'.encode(), b'bb'), 1733, 'missing_cjk_text'),
            (sample().replace(b'number="18"', b'number="17"'), 1733, 'page_sequence_mismatch'),
            (sample(), 1731, 'page_count_mismatch'),
            (b'\xff', 1733, 'invalid_xml_encoding'),
        ]
        for xml, issue, code in cases:
            with self.subTest(code=code), self.assertRaisesRegex(ProbeError, '^' + code + '$'):
                verify_xml(xml, issue)

    def test_does_not_leak_process_errors(self):
        with patch.object(Path, 'open', side_effect=OSError('private source path')):
            with self.assertRaisesRegex(ProbeError, '^poppler_failed$'):
                probe(Path('private.pdf'), 1733, 'pdftohtml')
        with patch.object(Path, 'open', side_effect=FileNotFoundError('private source path')):
            with self.assertRaisesRegex(ProbeError, '^missing_input_or_poppler$'):
                probe(Path('private.pdf'), 1733, 'pdftohtml')


if __name__ == '__main__':
    unittest.main()
