import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from pypdf import PdfWriter
from pypdf.generic import DictionaryObject, NameObject, DecodedStreamObject


class GlyphsTest(unittest.TestCase):
    def export(self, content):
        with tempfile.TemporaryDirectory() as root:
            writer = PdfWriter()
            page = writer.add_blank_page(612, 792)
            font = DictionaryObject({NameObject('/Type'): NameObject('/Font'), NameObject('/Subtype'): NameObject('/Type1'), NameObject('/BaseFont'): NameObject('/Helvetica')})
            page[NameObject('/Resources')] = DictionaryObject({NameObject('/Font'): DictionaryObject({NameObject('/F1'): writer._add_object(font)})})
            stream = DecodedStreamObject()
            stream.set_data(content)
            page[NameObject('/Contents')] = writer._add_object(stream)
            source = Path(root) / 'source.pdf'
            writer.write(source)
            result = subprocess.run([str(Path(__file__).with_name('glyphs')), str(source)], check=True, capture_output=True)
            self.assertEqual(result.stderr, b'')
            return json.loads(result.stdout)['pages'][0]

    def test_kerning_and_proportional_glyph_positions_are_native(self):
        page = self.export(b'BT /F1 20 Tf 1 0 0 1 72 700 Tm [(Wi) 100 (~Ada)] TJ ET')
        glyphs = page['glyphs']
        self.assertEqual(''.join(glyph['text'] for glyph in glyphs), 'Wi~Ada')
        for glyph, expected in zip(glyphs, [72, 90.88, 93.32, 105, 118.34, 129.46]):
            self.assertAlmostEqual(glyph['box']['x'] * 612, expected, places=2)

    def test_page_transform_is_applied_to_character_positions(self):
        page = self.export(b'q .5 0 0 .5 30 40 cm BT /F1 20 Tf 1 0 0 1 72 700 Tm (A) Tj ET Q')
        self.assertAlmostEqual(page['glyphs'][0]['box']['x'] * 612, 66, places=2)

    def test_invalid_source_fails_without_logging_private_input(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / 'private.pdf'
            source.write_bytes(b'PRIVATE invalid PDF')
            result = subprocess.run([str(Path(__file__).with_name('glyphs')), str(source)], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn(b'PRIVATE', result.stderr)
            self.assertNotIn(str(source).encode(), result.stderr)


if __name__ == '__main__':
    unittest.main()
