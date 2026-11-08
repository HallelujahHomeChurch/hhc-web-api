"""No personal data. Run with reportlab and pypdf installed."""
from pathlib import Path
from io import BytesIO
from reportlab.pdfgen import canvas
from reportlab.lib.utils import ImageReader
from PIL import Image
import math
from pypdf import PdfReader, PdfWriter
from pypdf.generic import DictionaryObject, NameObject, ArrayObject, NumberObject, TextStringObject, BooleanObject
root=Path(__file__).parent
# Procedurally drawn raster landscape, no third-party image or private original.
photo=Image.new('RGB',(600,300)); pixels=photo.load()
for y in range(300):
    for x in range(600):
        ridge=140+40*math.sin(x/63)+20*math.sin(x/21)
        pixels[x,y]=(25+int(x/12),60+int(y/6),100+int(x/9)) if y<ridge else (25+int(y/12),38+int(x/24),24+int(y/20))
buf=BytesIO(); c=canvas.Canvas(buf,pagesize=(612,792))
for i in range(7):
    c.setPageSize((792,612) if i==2 else (612,792))
    
    if i==1: c.drawImage(ImageReader(photo),0,0,width=612,height=792)
    c.setFont('Helvetica',16); c.drawString(40,560,'SYNTHETIC BULLETIN - PUBLIC TEST DATA')
    for col in (40,320):
        for row in range(28):
            c.setFont('Helvetica',9); c.drawString(col,530-row*16,'Searchable original text and column content.')
    c.setFillColorRGB(.08,.12,.2); c.rect(40,70,530,120,fill=1,stroke=0)
    c.setFillColorRGB(1,1,1); c.drawString(60,120,'Dark image-like background')
    c.setFillColorRGB(0,0,0)
    c.linkURL('https://example.invalid/no-network-fetch',(40,540,300,560))
    c.showPage()
c.save(); w=PdfWriter(); w.clone_document_from_reader(PdfReader(buf))
for i,rotation in ((3,90),(4,180),(5,270)): w.pages[i].rotate(rotation)
w.pages[6].cropbox.lower_left=(20,30); w.pages[6].cropbox.upper_right=(592,742)
w.write(root/'matrix.pdf')
w2=PdfWriter(); w2.add_page(w.pages[0]); w2.encrypt('', 'owner-secret'); w2.write(root/'encrypted.pdf')
w3=PdfWriter(); [w3.add_page(w.pages[0]) for _ in range(61)]; w3.write(root/'too-many-pages.pdf')
w4=PdfWriter(); w4.add_page(w.pages[0]); sig=DictionaryObject({NameObject('/Type'):NameObject('/Sig'),NameObject('/Filter'):NameObject('/Adobe.PPKLite'),NameObject('/SubFilter'):NameObject('/adbe.pkcs7.detached'),NameObject('/ByteRange'):ArrayObject([NumberObject(0),NumberObject(1),NumberObject(2),NumberObject(3)]),NameObject('/Contents'):TextStringObject('synthetic invalid signature')}); w4._root_object[NameObject('/Perms')]=DictionaryObject({NameObject('/DocMDP'):w4._add_object(sig)}); w4.write(root/'signed.pdf')
(root/'corrupt.pdf').write_bytes(b'%PDF-1.7\ninvalid\n%%EOF')
# Minimal tagged page: preserve original MCID, structure tree and MarkInfo.
w5=PdfWriter(); w5.add_page(w.pages[0]); page=w5.pages[0]; contents=page.get_contents(); contents.set_data(b'/P <</MCID 0>> BDC\n'+contents.get_data()+b'\nEMC'); page.replace_contents(contents); page[NameObject('/StructParents')]=NumberObject(0)
tree=DictionaryObject({NameObject('/Type'):NameObject('/StructTreeRoot')}); tree_ref=w5._add_object(tree)
elem=DictionaryObject({NameObject('/Type'):NameObject('/StructElem'),NameObject('/S'):NameObject('/P'),NameObject('/P'):tree_ref,NameObject('/Pg'):page.indirect_reference,NameObject('/K'):NumberObject(0)})
elem_ref=w5._add_object(elem); tree[NameObject('/K')]=ArrayObject([elem_ref]); tree[NameObject('/ParentTree')]=w5._add_object(DictionaryObject({NameObject('/Nums'):ArrayObject([NumberObject(0),ArrayObject([elem_ref])])})); tree[NameObject('/ParentTreeNextKey')]=NumberObject(1)
w5._root_object[NameObject('/StructTreeRoot')]=tree_ref; w5._root_object[NameObject('/MarkInfo')]=DictionaryObject({NameObject('/Marked'):BooleanObject(True)}); w5.write(root/'tagged.pdf')
(root/'truncated.pdf').write_bytes((root/'matrix.pdf').read_bytes().split(b'startxref')[0])

# Large fixtures are generated on demand and must not be committed.
if '--stress' in __import__('sys').argv:
    import random
    from reportlab import rl_config
    rl_config.useA85=False
    stress=BytesIO();sc=canvas.Canvas(stress,pagesize=(612,792),pageCompression=1)
    noise=Image.frombytes('RGB',(2500,2500),random.Random(91).randbytes(2500*2500*3))
    for _ in range(60):
        sc.drawImage(ImageReader(noise),0,0,width=612,height=792);sc.showPage()
    sc.save(); raw=stress.getvalue();offset=raw.rindex(b'startxref')
    padding=20*1024*1024-len(raw)
    assert padding>2, len(raw)
    raw=raw[:offset]+(b'%'+b' '*77+b'\n')*(padding//79)+b'\n'*(padding%79)+raw[offset:]
    assert len(raw)==20*1024*1024
    (root/'stress-20mib-60pages.pdf').write_bytes(raw)
