# Checkbox detection API

An HTTP service with one endpoint. Give it a document image or a PDF and it
reports where the checkboxes are and which ones are marked.

```
POST /detect     multipart/form-data, file field "image"
```

## Run it with Docker Compose

Nothing to install but Docker. The image brings its own OpenCV and poppler, so
none of the local setup below applies.

```sh
docker compose up --build             # foreground, Ctrl-C to stop
docker compose up -d --build          # or detached
docker compose logs -f
docker compose down
```

Then, from the repo root:

```sh
curl -X POST localhost:8080/detect -F "image=@testdata/form.png"
```

The same four commands are wrapped as `make up`, `make logs` and `make down`.

The container always listens on 8080; `PORT` moves the host side of the
mapping:

```sh
PORT=9000 docker compose up -d --build
curl -X POST localhost:9000/detect -F "image=@testdata/mixed.pdf"
```

Compose polls `/detect` with a GET until it answers 405, so `docker compose ps`
reports `healthy` only once the router is actually serving. The first build
takes a few minutes, mostly `apt-get`; later ones reuse the Go module and
build caches and take seconds.

### What is in the image

A two-stage build on Debian trixie, which packages OpenCV 4.10 and poppler, so
neither is compiled from source:

| stage | carries |
| --- | --- |
| builder | the Go toolchain, `libopencv-dev`, `pkg-config` |
| runtime | the OpenCV shared libraries, `poppler-utils` for `pdftoppm`, and the binary, running as a non-root user |

It comes out around a gigabyte, almost all of it OpenCV: gocv's root package
wraps every module, so the binary links all twelve of them. `.dockerignore`
keeps `testdata/` out of the build context, which also means the uncommitted
appraisal samples cannot reach an image.

The tests do not run in the container, and for day-to-day work the local
toolchain is faster to iterate with.

## Setup for local development

Go 1.27, plus two system dependencies:

```sh
brew install opencv@4 poppler

# opencv@4 is keg-only, so its opencv4.pc sits outside pkg-config's default
# search path. gocv's cgo directives ask for it by name, so without this
# symlink any build that has to compile gocv fails, and so does the IDE's own
# build, which does not inherit a run configuration's environment.
ln -sf /opt/homebrew/opt/opencv@4/lib/pkgconfig/opencv4.pc \
       /opt/homebrew/lib/pkgconfig/opencv4.pc
```

Homebrew's default `opencv` formula is 5.x, which gocv does not support; the
versioned `opencv@4` is required. `poppler` provides `pdftoppm`, which is only
needed for PDFs that have no form fields.

## Build and run

```sh
make run          # go run . -addr :8080
make run ADDR=:9000
make build        # -> bin/api
make test         # go test ./...
make samples      # go test -tags samples ./... (needs the uncommitted samples)
make race         # go test -race ./...
make check        # fmt + vet + race
make fixtures     # regenerate testdata from testdata/gen_fixtures.py
make up           # docker compose up -d --build
make down         # docker compose down
make logs         # docker compose logs -f
```

Every target exports `PKG_CONFIG_PATH` itself, so `make` works even without the
symlink above. The only flag is `-addr`.

Run tests through `make test` or `go test ./...` — never `go test some_test.go`,
which compiles that one file and fails with `undefined: NewDetector`.

`make samples` additionally runs the cases that read the challenge's own
appraisal documents. Those are someone else's paperwork and are not committed,
so the cases sit behind a `samples` build tag and fail rather than skip when
the files are absent — see `testdata/README.md`.

## Using it

```sh
curl -X POST localhost:8080/detect -F "image=@testdata/form.png"
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
    { "bbox": [40, 40, 68, 68], "is_checked": false, "confidence": 0.96 },
    { "bbox": [140, 40, 168, 68], "is_checked": true, "confidence": 0.96 }
  ]
}
```

`bbox` is `[x1, y1, x2, y2]` in pixels from the image's top-left. The second
corner is **exclusive**: `width == x2 - x1`, so a 28px box at (40,40) is
`[40, 40, 68, 68]`.

Per box, beyond the two required fields:

| field | meaning |
| --- | --- |
| `confidence` | 0-1. Shape-fit score from the detector, or exactly `1` for a PDF that states its own answer. |
| `page` | 1-based page of a PDF. Omitted for images. |
| `name` | The PDF's qualified form field name, e.g. `consent.marketing`. Omitted unless the answer came from form fields. |

And at the top level, `method` says where the answer came from: `acroform` when
read out of a PDF's own form fields, `pixels` when the detector looked at the
image. For PDFs, `image.pages` counts every page in the document, even when
only the first ten were scanned.

### Accepted uploads

JPEG, PNG, WebP, GIF and PDF. The type is decided by **sniffing the bytes**,
not by the client's declared `Content-Type`, so a shell script sent as
`image/png` is rejected.

For best results scan at 200-300 DPI. Below roughly 12px a checkbox and a
letter are both just blobs (see the writeup).

### Errors

| status | when |
| --- | --- |
| 400 | body is not `multipart/form-data`, or has no `image` file field |
| 413 | upload over 10 MiB, or an image over 24 million pixels |
| 415 | not a supported type, or the bytes are malformed |
| 422 | the PDF could not be read at all |
| 500 | the image decoded but could not be examined |
| 503 | a scanned PDF arrived but `pdftoppm` is not installed |
| 504 | the PDF took longer than the 30s request timeout |

## Layout

| file | |
| --- | --- |
| `main.go` | chi router, middleware, flags, graceful shutdown |
| `detect.go` | the endpoint: multipart handling, sniffing, limits, response shape |
| `detector.go` | the image detector (OpenCV via gocv) |
| `pdf.go` | PDF handling: AcroForm fast path, pdftoppm fallback, page geometry |
| `testdata/` | fixtures, all generated by `gen_fixtures.py`; see `testdata/README.md` |
| `api.http` | requests for the endpoint, runnable from the IDE with assertions |
| `Dockerfile`, `compose.yaml` | the containerised build: OpenCV and poppler from Debian trixie |

`WRITEUP.md` covers the approach, the tradeoffs and the known limitations.
