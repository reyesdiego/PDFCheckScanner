# Test fixtures

`gen_fixtures.py` builds the PDF fixtures. Rerun it from the repo root after
changing what they should contain:

    python3 testdata/gen_fixtures.py

| file | purpose |
| --- | --- |
| `form-fields.pdf` | AcroForm checkboxes on three pages (page 3 is `/Rotate 90`), one of them a widget nested under a parent field so its qualified name is `consent.marketing`, plus a radio group, a push button and a text field that must all be ignored. Exercises the fast path. |
| `scanned-form.pdf` | One flattened grayscale page, 8 checkboxes, 3 marked. No form fields, so it exercises the pdftoppm fallback. |
| `form.png` | A small form image, 4 checkboxes with the middle two marked. Used by `api.http`. |
| `scanned-broken-fields.pdf` | The same page with a form field pdfcpu rejects as invalid. Exercises falling through from a failed field read to the rasterizer. |

`lossless.webp` (VP8L) and `lossy.webp` (VP8) are copied from the
golang.org/x/image test suite (`testdata/gopher-doc.1bpp.lossless.webp` and
`testdata/blue-purple-pink.lossy.webp`), which is BSD-licensed:
https://cs.opensource.google/go/x/image/+/master:LICENSE

They exist because the standard library has no WebP encoder, so a real WebP
byte stream cannot be generated from within the tests.
