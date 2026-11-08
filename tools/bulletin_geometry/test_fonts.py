import io
import struct
import unittest

from pypdf import PdfReader, PdfWriter
from pypdf.generic import DictionaryObject, NameObject, DecodedStreamObject

from fonts import font_names, page_fonts


def font_data(name):
    encoded = name.encode('utf-16-be')
    table = struct.pack('>HHH', 0, 1, 18) + struct.pack('>HHHHHH', 3, 1, 0x409, 1, len(encoded), 0) + encoded
    return struct.pack('>IHHHH', 0x10000, 1, 0, 0, 0) + struct.pack('>4sIII', b'name', 0, 28, len(table)) + table


class FontsTest(unittest.TestCase):
    def test_embedded_names_are_bound_to_page_and_pdf_alias(self):
        writer = PdfWriter()
        for name in ['DFKai-SB', '全真顏體<']:
            page = writer.add_blank_page(612, 792)
            stream = DecodedStreamObject()
            stream.set_data(font_data(name))
            descriptor = DictionaryObject({NameObject('/FontFile2'): writer._add_object(stream)})
            font = DictionaryObject({NameObject('/BaseFont'): NameObject('/CIDFont+F1'), NameObject('/FontDescriptor'): descriptor})
            page[NameObject('/Resources')] = writer._add_object(DictionaryObject({NameObject('/Font'): writer._add_object(DictionaryObject({NameObject('/F1'): font}))}))
        output = io.BytesIO()
        writer.write(output)
        self.assertEqual(page_fonts(PdfReader(output)), [
            {'CIDFont+F1': ['DFKai-SB']}, {'CIDFont+F1': ['全真顏體<']}])

    def test_malformed_or_unrecognized_sfnt_is_not_guessed(self):
        for value in [b'', b'OTTO' + b'\0' * 50, font_data('DFKai-SB')[:-1]]:
            self.assertEqual(font_names(value), [])


if __name__ == '__main__':
    unittest.main()
