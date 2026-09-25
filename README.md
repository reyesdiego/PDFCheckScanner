# Checkbox detection API

**Finds the checkboxes on a form and says which ones are ticked.** Send a
scanned page or a PDF; get back every checkbox's position and whether it is
marked, as JSON. That turns the yes/no answers on a paper form, such as an
appraisal report, into data without a person reading the page.

```text
POST /detect     multipart/form-data, file field "image"
->  { "boxes": [ { "bbox": [140, 40, 168, 68], "is_checked": true }, ... ] }
```

What makes it more than a demo:

- **Exact answers when the PDF has them.** A fillable PDF is answered from its
  own form fields, with each box's field name, instead of being guessed from
  pixels.
- **Computer vision for everything else.** Scanned images and flattened PDFs
  go through an OpenCV detector that looks for four-cornered outlines,
  not square blobs of ink, so letters like `D` and `o` are not reported as
  boxes.
- **Strict at the edge.** File types are checked from the bytes, sizes and
  pixel counts are capped, and a rejected upload gets a JSON error whose
  status code says what kind of problem it was.
- **One command to run:** `docker compose up --build`.

Where to read next:

| | |
| --- | --- |
| [Start it](#start-it), [Using it](#using-it) | run the service and call it |
| [Limitations](#limitations) | what it gets wrong, and edge cases |
| [docs/PIPELINE.md](docs/PIPELINE.md) | how a checkbox is found, with figures |
| [WRITEUP.md](WRITEUP.md) | design decisions, tradeoffs, next steps |

The code is also on GitHub, with its full history and the latest version:
**https://github.com/reyesdiego/PDFCheckScanner**.

## Start it

Only Docker is needed; the image brings its own OpenCV and poppler.

```sh
docker compose up --build        # serves on http://localhost:8080; Ctrl-C stops it
```

Then, from the repo root in another shell:

```sh
curl -X POST localhost:8080/detect -F "image=@testdata/form.png"
```

```json
{
  "boxes": [
    { "bbox": [40, 40, 68, 68], "is_checked": false },
    { "bbox": [140, 40, 168, 68], "is_checked": true },
    { "bbox": [240, 40, 268, 68], "is_checked": true },
    { "bbox": [340, 40, 368, 68], "is_checked": false }
  ]
}
```

That fixture has four checkboxes, the middle two marked. More requests are
under [Trying it out](#trying-it-out).

- **In the background:** `make up`, `make logs`, `make down`.
- **Another port:** `PORT=9000 docker compose up --build`.
- **Build time:** the first build takes a few minutes, later ones seconds.
  `docker compose ps` shows `healthy` once the service is answering.

The image is a two-stage build on Debian trixie, which packages OpenCV 4.10 and
poppler. It is about 1 GB, nearly all OpenCV, and runs as a non-root user.
`testdata/` is kept out of the build, and the tests run locally, not in the
container.

## How it works

![Checkboxes found on a page of an appraisal report](docs/img/05-result.png)

Green is marked, red is not. A PDF with form fields is answered from those
fields. Everything else is rasterized at 300 DPI and put through a detector
that looks for **quadrilaterals, not square-ish blobs**, because `D` and `o`
are as square as a checkbox.

**[docs/PIPELINE.md](docs/PIPELINE.md) walks through each step with
figures**, including the three bugs real appraisal pages exposed. The figures
are drawn by the detector itself (`make docs`), so they cannot drift from the
code.

## Limitations

The detector is geometry and thresholds, not a trained model, and it has only
been checked against a handful of real pages. What it is known to get wrong:

- **Skewed pages lose boxes.** Boxes are assumed roughly axis-aligned and there
  is no deskew step, so photographed or crooked scans do poorly.
- **Small boxes are not found.** Below about 12px a checkbox and a letter look
  the same. Scan at 200-300 DPI; nothing warns you when an image is too coarse.
- **Boxes touching a table rule can be missed.** The border merges into the
  grid, and the box is found only by its empty interior, which a mark breaks
  up. On one sample page this hides about a third of the marked boxes.
- **Square table cells can be reported as checkboxes** when they are the same
  size and shape.
- **A pen stroke through a box loses it**, and so does a border broken worse
  than the samples' own damage.
- **Faint marks away from the centre**, such as light pencil, can read as
  unmarked.
- **`confidence` is a ranking, not a probability.** It separates real boxes
  from look-alikes well, but it is not calibrated, so there is no principled
  cutoff to filter on.
- **Accuracy is not measured.** There are no labelled pages, so the counts
  quoted here are regression floors checked by eye, not precision and recall.

Some inputs get a deliberate answer rather than an error:

| input | answer |
| --- | --- |
| an image that is more than 60% ink, e.g. an inverted scan | `200` with no boxes |
| a page with no checkboxes | `200` with no boxes |
| a PDF longer than 10 pages | boxes from the first 10; `image.pages` still counts all of them |
| a PDF page too large to render at 300 DPI | rendered smaller, with its boxes scaled back to 300 DPI coordinates |
| a PDF with at least one checkbox form field | answered from its fields alone; no page is scanned, so checkboxes that are only printed are not reported |
| a PDF whose form fields cannot all be read | scanned as pixels instead, and the reason logged |

The full list, with the cases behind each item and what was tried, is in
[WRITEUP.md](WRITEUP.md#known-limitations).

## Setup for local development

Go 1.27, plus OpenCV 4 and poppler:

```sh
brew install opencv@4 poppler

# opencv@4 is keg-only; this puts it where gocv's build, and the IDE's, look.
ln -sf /opt/homebrew/opt/opencv@4/lib/pkgconfig/opencv4.pc \
       /opt/homebrew/lib/pkgconfig/opencv4.pc
```

It must be `opencv@4`: Homebrew's plain `opencv` is 5.x, which gocv does not
support. `poppler` provides `pdftoppm`, needed only for PDFs without form
fields.

## Build and run

```sh
make run          # go run . -addr :8080
make run ADDR=:9000
make build        # -> bin/api
make test         # go test ./...
make race         # go test -race ./...
make check        # fmt + vet + race
make smoke        # end-to-end checks against a running server
make docs         # regenerate the figures in docs/img
make fixtures     # regenerate testdata from testdata/gen_fixtures.py
make up           # docker compose up -d --build
make down         # docker compose down
make logs         # docker compose logs -f
```

Every target sets `PKG_CONFIG_PATH` itself, so `make` works without the
symlink. Run tests as a package (`make test` or `go test ./...`); `go test
some_test.go` compiles one file and fails with `undefined: NewDetector`.

## Using it

The default answer is only the boxes, as shown under [Start it](#start-it).
Add `?detail=true` (or a bare `?detail`) to also see how it was reached:

```sh
curl -X POST "localhost:8080/detect?detail=true" -F "image=@testdata/form.png"
```

```json
{
  "request_id": "host/AKAvIwek35-000001",
  "image": {
    "filename": "form.png",
    "content_type": "image/png",
    "size_bytes": 346,
    "width": 420,
    "height": 110
  },
  "method": "pixels",
  "boxes": [
    { "bbox": [40, 40, 68, 68], "is_checked": false, "confidence": 0.98 },
    { "bbox": [140, 40, 168, 68], "is_checked": true, "confidence": 0.98 }
  ]
}
```

`bbox` is `[x1, y1, x2, y2]` in pixels from the top-left, with the second
corner **exclusive**: a 28px box at (40,40) is `[40, 40, 68, 68]`.

| field | meaning | when |
| --- | --- | --- |
| `bbox`, `is_checked` | where the box is and whether it is marked | always |
| `page` | 1-based PDF page | PDFs only |
| `name` | the PDF's form field name, e.g. `consent.marketing` | form-field answers only |
| `confidence` | 0-1 shape-fit score; exactly `1` from form fields | `?detail=true` |
| `method` | `acroform` (form fields) or `pixels` (detector) | `?detail=true` |
| `image` | the upload; for PDFs, `pages` counts every page | `?detail=true` |
| `request_id` | matches the server log | `?detail=true` |

### Accepted uploads

JPEG, PNG, WebP, GIF and PDF. The type is decided by **sniffing the bytes**,
not the declared `Content-Type`, so a shell script sent as `image/png` is
rejected.

### Errors

Every error from `/detect` is JSON with a single `error` field. For example, a
text file sent as an image:

```sh
curl -i -X POST localhost:8080/detect -F "image=@README.md;type=image/png"
```

```http
HTTP/1.1 415 Unsupported Media Type
Content-Type: application/json

{"error":"unsupported type \"text/plain\", want one of: application/pdf, image/gif, image/jpeg, image/png, image/webp"}
```

| status | when | `error` |
| --- | --- | --- |
| 400 | the body is not `multipart/form-data` | `request must be multipart/form-data with an "image" file field` |
| 400 | there is no `image` file field | `missing "image" file field` |
| 413 | the upload is over 10 MiB | `upload must be at most 10485760 bytes` |
| 413 | the image is over 24 million pixels | `image must be at most 24000000 pixels, got 5000x5000` |
| 415 | the file is empty | `uploaded file is empty` |
| 415 | the file is not a supported type | `unsupported type "text/plain", want one of: ...` |
| 415 | the image is malformed or truncated | `could not decode the uploaded image` |
| 422 | the PDF cannot be rendered | `could not read the uploaded pdf` |
| 500 | the server failed while examining the upload | `could not examine the uploaded image` or `... pdf` |
| 500 | the server could not write the upload to a temp file | `could not buffer the upload` or `... uploaded pdf` |
| 503 | a scanned PDF arrived and `pdftoppm` is not installed | `pdftoppm is not installed, so scanned PDFs cannot be rasterized` |
| 504 | the PDF took longer than the 30s request timeout | `pdf took too long to process` |

A 500 is the server's fault. Its cause goes to the server log with the
request ID, never to the client. Branch on the status code; the
messages are for people and may change. The
router's own 405 (a method other than `POST`) and 404 (any other path) have no
JSON body.

## Trying it out

With the server running (`make run` or `make up`):

```sh
# an image: 4 checkboxes, the middle two marked
curl -X POST localhost:8080/detect -F "image=@testdata/form.png"

# a real appraisal page: 48 checkboxes, 12 of them marked
curl -X POST localhost:8080/detect -F "image=@testdata/image2.png"

# a ~150 DPI scan with 16-17px boxes: at least 55 found
curl -X POST localhost:8080/detect -F "image=@testdata/image1.png"

# a landscape appraisal page with a lettered section banner
curl -X POST localhost:8080/detect -F "image=@testdata/image3.png"

# a dense page: at least 110 boxes, none of them banner letters
curl -X POST localhost:8080/detect -F "image=@testdata/image4.png"

# a PDF with real form fields: answered from the AcroForm, not the pixels
curl -X POST localhost:8080/detect -F "image=@testdata/form-fields.pdf"

# a flattened PDF: pdftoppm renders it and the detector runs
curl -X POST localhost:8080/detect -F "image=@testdata/scanned-form.pdf"

# a page of text and no checkboxes: the false-positive canary, expect []
curl -X POST localhost:8080/detect -F "image=@testdata/prose.pdf"
```

`jq` makes the answers easier to read:

```sh
curl -s -X POST localhost:8080/detect -F "image=@testdata/mixed.pdf" \
  | jq '{total: (.boxes | length), checked: [.boxes[] | select(.is_checked)] | length}'
# { "total": 6, "checked": 3 }

curl -s -X POST "localhost:8080/detect?detail=true" -F "image=@testdata/form-fields.pdf" \
  | jq '.method, [.boxes[] | {name, is_checked}]'
# "acroform", with the PDF's own field names
```

### Automated checks

`make smoke` runs `scripts/smoke.sh`, which sends these requests and the error
cases, checks every answer, and exits non-zero if any check fails, so it can
gate a deploy. It needs `curl` and `jq`; `HOST=http://localhost:9000` points it
elsewhere. A failure shows what it expected:

```text
FAIL image2.png: 48 boxes, 12 of them marked
       want: 48 12
        got: 51 12
```

`api.http` has the same requests and checks for GoLand: run each with the ▶ in
the gutter, and the results appear in the run window's **Tests** tab, not in
the response body. VS Code's REST Client sends the requests but ignores the
checks.

## Layout

| file | |
| --- | --- |
| `main.go` | chi router, middleware, flags, graceful shutdown |
| `detect.go` | the endpoint: multipart handling, sniffing, limits, response shape |
| `detector.go` | the image detector (OpenCV via gocv) |
| `pdf.go` | PDF handling: AcroForm fast path, pdftoppm fallback, page geometry |
| `testdata/` | fixtures: generated forms and real appraisal pages; see `testdata/README.md` |
| `api.http` | requests for the endpoint, runnable from the IDE with checks |
| `Dockerfile`, `compose.yaml` | the container build: OpenCV and poppler from Debian trixie |
