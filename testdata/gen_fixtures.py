#!/usr/bin/env python3
"""Generates the fixtures used by the /detect tests and by api.http.

Run from the repo root:  python3 testdata/gen_fixtures.py

form-fields.pdf   an AcroForm with checkboxes, a radio group, a push button
                  and a text field, across three pages (page 3 is /Rotate 90),
                  to exercise the fast path and its field filtering.
scanned-form.pdf  a flattened page: one grayscale image of a form, no fields,
                  which forces the pdftoppm + detector fallback.
form.png          a small form image, for api.http to post.
"""
import struct
import zlib

def build(objects, root):
    """Assembles numbered objects into a PDF with a correct xref table."""
    out = bytearray(b"%PDF-1.7\n")
    offsets = {}
    for num, body in objects:
        offsets[num] = len(out)
        out += f"{num} 0 obj\n".encode() + body + b"\nendobj\n"
    xref = len(out)
    n = max(offsets) + 1
    out += f"xref\n0 {n}\n".encode() + b"0000000000 65535 f \n"
    for i in range(1, n):
        out += f"{offsets[i]:010d} 00000 n \n".encode()
    out += f"trailer\n<</Size {n}/Root {root} 0 R>>\nstartxref\n{xref}\n%%EOF\n".encode()
    return bytes(out)

def stream(d, data):
    return f"<<{d}/Length {len(data)}>>\nstream\n".encode() + data + b"\nendstream"

# ---------------------------------------------------------------- AcroForm
def widget(name, rect, on=None, extra=""):
    state = f"/{on}" if on else "/Off"
    return (f"<</Type/Annot/Subtype/Widget/FT/Btn/T({name})/V{state}/AS{state}"
            f"/Rect[{rect}]/F 4{extra}>>").encode()

objs = [
    (1, b"<</Type/Catalog/Pages 2 0 R/AcroForm<</Fields[5 0 R 6 0 R 7 0 R 10 0 R 11 0 R 12 0 R 13 0 R]"
        b"/DA(/Helv 0 Tf 0 g)/DR<</Font<</Helv 4 0 R>>>>>>>>"),
    (2, b"<</Type/Pages/Kids[3 0 R 8 0 R 9 0 R]/Count 3>>"),
    (3, b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<<>>"
        b"/Annots[5 0 R 6 0 R 10 0 R 11 0 R 12 0 R]>>"),
    (4, b"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>"),
    # checked, unchecked
    (5, widget("agree", "100 700 118 718", on="Yes")),
    (6, widget("subscribe", "100 660 118 678")),
    # page 3 is rotated; the widget rect is in unrotated PDF space
    (7, widget("third", "100 700 118 718", on="On")),
    (8, b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<<>>/Annots[14 0 R]>>"),
    (9, b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Rotate 90/Resources<<>>"
        b"/Annots[7 0 R]>>"),
    # a radio group, a push button and a text field: all must be ignored
    (10, widget("gender", "100 600 118 618", on="M", extra="/Ff 32768")),
    (11, widget("submit", "400 600 460 620", extra="/Ff 65536")),
    (12, b"<</Type/Annot/Subtype/Widget/FT/Tx/T(fullname)/V(Ada)/DA(/Helv 0 Tf 0 g)"
         b"/Rect[100 540 300 560]/F 4>>"),
    # a field whose widget is a kid: type, value and name prefix are inherited,
    # so the reported name must be the qualified "consent.marketing"
    (13, b"<</FT/Btn/T(consent)/V/Yes/Kids[14 0 R]>>"),
    (14, b"<</Type/Annot/Subtype/Widget/Parent 13 0 R/T(marketing)/AS/Yes"
         b"/Rect[200 500 218 518]/F 4>>"),
]
open("testdata/form-fields.pdf", "wb").write(build(objs, 1))
print("form-fields.pdf")

# ------------------------------------------------------------ scanned page
W, H = 1275, 1650          # letter at 150 DPI
px = bytearray(b"\xff" * (W * H))

def ink(x, y):
    if 0 <= x < W and 0 <= y < H:
        px[y * W + x] = 0


def checkbox(x, y, side, stroke, checked):
    for t in range(stroke):
        for i in range(side):
            ink(x + i, y + t); ink(x + i, y + side - 1 - t)
            ink(x + t, y + i); ink(x + side - 1 - t, y + i)
    if checked:
        o = side // 4
        for i in range(side - 2 * o):
            for t in range(2):
                ink(x + o + i, y + o + i + t)
                ink(x + side - o - i, y + o + i + t)

CHECKED = {0, 3, 5}
for i in range(8):
    checkbox(150, 200 + i * 150, 26, 2, i in CHECKED)

img = stream("/Type/XObject/Subtype/Image/Width %d/Height %d"
             "/ColorSpace/DeviceGray/BitsPerComponent 8/Filter/FlateDecode" % (W, H),
             zlib.compress(bytes(px), 9))
content = b"q 612 0 0 792 0 0 cm /Im0 Do Q"
objs = [
    (1, b"<</Type/Catalog/Pages 2 0 R>>"),
    (2, b"<</Type/Pages/Kids[3 0 R]/Count 1>>"),
    (3, b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]"
        b"/Resources<</XObject<</Im0 5 0 R>>>>/Contents 4 0 R>>"),
    (4, stream("", content)),
    (5, img),
]
open("testdata/scanned-form.pdf", "wb").write(build(objs, 1))
print("scanned-form.pdf: 8 boxes,", len(CHECKED), "checked")

# ------------------------------------------- scanned page + unreadable fields
# The text field has no /DA, which pdfcpu rejects as invalid. Poppler renders
# the page regardless, so this exercises the fall-through from the failed field
# read to the rasterizer.
objs = [
    (1, b"<</Type/Catalog/Pages 2 0 R/AcroForm<</Fields[6 0 R]>>>>"),
    (2, b"<</Type/Pages/Kids[3 0 R]/Count 1>>"),
    (3, b"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]"
        b"/Resources<</XObject<</Im0 5 0 R>>>>/Contents 4 0 R/Annots[6 0 R]>>"),
    (4, stream("", content)),
    (5, img),
    (6, b"<</Type/Annot/Subtype/Widget/FT/Tx/T(notes)/V(x)/Rect[400 100 500 120]/F 4>>"),
]
open("testdata/scanned-broken-fields.pdf", "wb").write(build(objs, 1))
print("scanned-broken-fields.pdf: fields unreadable, 8 boxes in the image")


# ------------------------------------------------------------- plain image
def write_png(path, w, h, px):
    raw = b"".join(b"\x00" + bytes(px[y * w:(y + 1) * w]) for y in range(h))
    def chunk(t, d):
        c = t + d
        return struct.pack(">I", len(d)) + c + struct.pack(">I", zlib.crc32(c) & 0xFFFFFFFF)
    open(path, "wb").write(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 0, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(raw, 9))
        + chunk(b"IEND", b""))

W, H = 420, 110
px = bytearray(b"\xff" * (W * H))
IMG_CHECKED = {1, 2}
for i in range(4):
    checkbox(40 + i * 100, 40, 28, 2, i in IMG_CHECKED)
write_png("testdata/form.png", W, H, px)
print("form.png: 4 boxes,", len(IMG_CHECKED), "checked")
