# Test fixtures

`gen_fixtures.py` builds the PDFs and `form.png`. Rerun it from the repo root
after changing what they should contain:

```sh
python3 testdata/gen_fixtures.py        # or: make fixtures
```

`image1.png` to `image4.png` are real appraisal pages, and the two WebP files
are copied in; see the end of this file.

| file | purpose |
| --- | --- |
| `form-fields.pdf` | AcroForm checkboxes on three pages (page 3 is `/Rotate 90`), one of them a widget nested under a parent field so its qualified name is `consent.marketing`, plus a radio group, a push button and a text field that must all be ignored. Exercises the fast path. |
| `scanned-form.pdf` | One flattened grayscale page, 8 checkboxes, 3 marked. No form fields, so it exercises the pdftoppm fallback. |
| `form.png` | A small form image, 4 checkboxes with the middle two marked. Used by `api.http` and by the thin-mark test. No text: see the note below. |
| `prose.pdf` | Real Helvetica text, a bold heading, and not one checkbox. The false-positive canary. |
| `mixed.pdf` | The same text plus 6 checkboxes, 3 of them marked, so precision and recall are exercised together. |
| `scanned-broken-fields.pdf` | The same page with a form field pdfcpu rejects as invalid. Exercises falling through from a failed field read to the rasterizer. |
| `image1.png` | A manufactured-home appraisal page at about 150 DPI, 1120x1694, where a checkbox is 16-17px. The page that exposed the polygon tolerance being finer than the raster: a third of its boxes were rejected for fitting a pentagon. Also the clearest case of the table-rule limitation, since its marked boxes are fused to the form grid. |
| `image2.png` | An appraisal page, 1198x1840, with 48 checkboxes of which 12 are marked. Each checkbox has a narrow ruled cell beside it, 17-19px against the boxes' 25px, and all 13 of them were reported as checkboxes. |
| `image3.png` | Another appraisal page, 1412x816. No test of its own; further real-document input. |
| `lossless.webp`, `lossy.webp` | One of each WebP encoding, to prove both decode. |
| `image4.png` | A dense appraisal page, 1118x1820, with 118 checkboxes of 25-27px. The page that exposed the section banners: the word set vertically down the margin in white on black had ten of its letters read as checked boxes. Nine ruled table cells holding a word, 32-39px wide, are still reported and are the known remainder. |

Both `api.http` and `scripts/smoke.sh` post these fixtures at a running
server and assert on the answers, so their expectations have to move together
with this table.

## Why the text fixtures are PDFs

Everything drawn by hand here is a rectangle on blank paper, and for a long
while that was all the fixtures contained. The suite passed while the detector
was reporting 167 checkboxes on a page of prose, because nothing in `testdata`
had any text in it.

Drawing convincing glyphs by hand does not work: what breaks a geometric
checkbox detector is specifically letters with straight stems and
near-rectangular counters — `B`, `D`, `O`, `0`, `8` — and crude approximations
of those do not reproduce the failure. Rasterizing real type does, and a PDF
gets it for free: the content stream names Helvetica, and `pdftoppm` renders
genuine anti-aliased glyphs at whatever resolution the detector asks for. No
font file, no font rasterizer, no extra dependency.

That is also why `form.png` has no text on it. There is no image library here
that can draw type into a bitmap, so the text fixtures take the PDF route.

`lossless.webp` (VP8L) and `lossy.webp` (VP8) are copied from the
golang.org/x/image test suite (`testdata/gopher-doc.1bpp.lossless.webp` and
`testdata/blue-purple-pink.lossy.webp`), which is BSD-licensed:
https://cs.opensource.google/go/x/image/+/master:LICENSE

They exist because the standard library has no WebP encoder, so a real WebP
byte stream cannot be generated from within the tests.
