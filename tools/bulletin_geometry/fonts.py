"""Extract embedded font names only; never export font bytes or page content."""
import json
import logging
import resource
import struct
import sys

from pypdf import PdfReader


def font_names(data):
    if len(data) < 12 or len(data) > 16 << 20 or data[:4] not in (b'\0\1\0\0', b'true'):
        return []
    count = struct.unpack_from('>H', data, 4)[0]
    if count > 256 or 12 + count * 16 > len(data):
        return []
    for offset in range(12, 12 + count * 16, 16):
        tag, _, start, size = struct.unpack_from('>4sIII', data, offset)
        if tag != b'name':
            continue
        if size < 6 or start + size > len(data):
            return []
        table = data[start:start + size]
        fmt, entries, strings = struct.unpack_from('>HHH', table)
        if fmt not in (0, 1) or entries > 1024 or 6 + entries * 12 > len(table):
            return []
        names = set()
        for position in range(6, 6 + entries * 12, 12):
            platform, encoding, _, name_id, length, relative = struct.unpack_from('>HHHHHH', table, position)
            if name_id not in (1, 6) or length > 512:
                continue
            begin, end = strings + relative, strings + relative + length
            if begin < 6 + entries * 12 or end > len(table):
                return []
            if platform == 0 or (platform == 3 and encoding in (0, 1, 10)):
                codec = 'utf-16-be'
            elif platform == 1 and encoding == 0:
                codec = 'mac-roman'
            else:
                continue
            try:
                name = table[begin:end].decode(codec).strip()
            except UnicodeError:
                continue
            if name and len(name) <= 128 and all(c.isprintable() for c in name):
                names.add(name)
        return sorted(names)
    return []


def page_fonts(reader):
    if len(reader.pages) > 40:
        raise ValueError('page_limit')
    pages = []
    for page in reader.pages:
        fonts, seen = {}, set()

        def visit(resources, depth=0):
            if depth > 12 or len(seen) > 1024:
                raise ValueError('resource_limit')
            resources = resources.get_object()
            if id(resources) in seen:
                return
            seen.add(id(resources))
            for ref in resources.get('/Font', {}).values():
                font = ref.get_object()
                alias = str(font.get('/BaseFont', '')).removeprefix('/')
                if not alias or len(alias) > 128:
                    continue
                descendant = font.get('/DescendantFonts', [font])[0].get_object()
                descriptor = descendant.get('/FontDescriptor', {})
                descriptor = descriptor.get_object() if hasattr(descriptor, 'get_object') else descriptor
                stream = descriptor.get('/FontFile2')
                names = font_names(stream.get_object().get_data()) if stream else []
                # One alias naming different font resources is not safe evidence.
                fonts[alias] = names if alias not in fonts or fonts[alias] == names else []
                if len(fonts) > 256:
                    raise ValueError('font_limit')
            for ref in resources.get('/XObject', {}).values():
                obj = ref.get_object()
                if obj.get('/Subtype') == '/Form' and '/Resources' in obj:
                    visit(obj['/Resources'], depth + 1)

        visit(page.get('/Resources', {}))
        pages.append(fonts)
    return pages


if __name__ == '__main__':
    resource.setrlimit(resource.RLIMIT_AS, (384 << 20, 384 << 20))
    resource.setrlimit(resource.RLIMIT_CPU, (30, 30))
    logging.disable(logging.CRITICAL)
    try:
        result = json.dumps(page_fonts(PdfReader(sys.argv[1])), ensure_ascii=True)
        if len(result) > 64 << 10:
            raise ValueError('output_limit')
        print(result)
    except Exception:
        sys.exit(1)  # Never log private PDF content or paths.
