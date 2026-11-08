# Synthetic bulletin parser corpus

Only synthetic PDFs/goldens belong here. No original sermon/member text, fonts,
images, QR codes or original PDF metadata is copied. Source page count, paper
boxes, text slot coordinates, size classes and hymn columns come from the locally
reviewed 1739/1740 originals. The lower back-page service/attendance/offering
region is replaced by an empty labeled rectangle.

Text uses fixed tokens from `tools/bulletin_geometry/fixtures.py`; image data must
be exactly one of two uniform 16x16 RGB pixel patterns. The privacy scan rejects
other text, fonts, image pixels, metadata, links and attachments. Goldens retain
synthetic decoded text, ordered boxes, image checksums and Poppler version only.

The legal substitute font is Noto Sans TC, licensed under SIL OFL 1.1. The font
is subset-embedded by ReportLab; no source font file is committed. `OFL.txt`
includes the full copyright/license notice. Source:

https://github.com/google/fonts/tree/main/ofl/notosanstc

Input font SHA-256:
`864727d210d54f2537bbe23b3a839436c3992af72de9322af5270897246bd44f`

Its embedded default face is `NotoSansTC-Thin`. This controlled parser font is
not the final reader typography choice. Production body/scripture/emphasis
fonts are selected and visually accepted in the template/renderer tasks.
