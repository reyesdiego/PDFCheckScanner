package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func previewOf(t *testing.T, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, uploadRequest(t, "/preview", imageField, filename, content))
	return rec
}

func decodePreview(t *testing.T, rec *httptest.ResponseRecorder) []pagePreview {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %.300s", rec.Code, rec.Body)
	}
	var got previewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Pages
}

// jpegOf decodes a preview's data URL, checking it is what it claims to be.
func jpegOf(t *testing.T, p pagePreview) image.Image {
	t.Helper()
	const prefix = "data:image/jpeg;base64,"
	if !strings.HasPrefix(p.Image, prefix) {
		t.Fatalf("page %d: image is not a JPEG data URL: %.40s", p.Page, p.Image)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p.Image, prefix))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("page %d: %v", p.Page, err)
	}
	if b := img.Bounds(); b.Dx() != p.Width || b.Dy() != p.Height {
		t.Errorf("page %d: image is %dx%d, preview says %dx%d", p.Page, b.Dx(), b.Dy(), p.Width, p.Height)
	}
	return img
}

// A letter page at previewDPI, and the scale that takes rasterDPI boxes onto
// it. The rotated third page of form-fields.pdf comes back landscape, which
// is what the acroform path's coordinates already assume.
func TestPreviewDrawsEachPage(t *testing.T) {
	requireRasterizer(t)

	pages := decodePreview(t, previewOf(t, "form.pdf", uploadFile(t, "testdata/form-fields.pdf")))
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3", len(pages))
	}
	for i, p := range pages {
		if p.Page != i+1 {
			t.Errorf("preview %d is page %d", i, p.Page)
		}
		if math.Abs(p.Scale-1.0/3) > 1e-9 {
			t.Errorf("page %d: scale = %v, want 1/3", p.Page, p.Scale)
		}
		jpegOf(t, p)
	}
	for _, p := range pages[:2] {
		if p.Width != 850 || p.Height != 1100 {
			t.Errorf("page %d: %dx%d, want 850x1100", p.Page, p.Width, p.Height)
		}
	}
	if p := pages[2]; p.Width != 1100 || p.Height != 850 {
		t.Errorf("rotated page 3: %dx%d, want 1100x850", p.Width, p.Height)
	}
}

// The outlines the upload page draws are only right if a box's rasterDPI
// coordinates, scaled, land on the printed box in the preview.
func TestPreviewScaleLandsBoxesOnThePage(t *testing.T) {
	requireRasterizer(t)

	dets, _, _, err := detectPDF(context.Background(), "testdata/scanned-form.pdf")
	if err != nil {
		t.Fatal(err)
	}
	pages := decodePreview(t, previewOf(t, "scan.pdf", uploadFile(t, "testdata/scanned-form.pdf")))
	if len(pages) != 1 || len(dets) != 8 {
		t.Fatalf("got %d pages and %d boxes, want 1 and 8", len(pages), len(dets))
	}
	img := jpegOf(t, pages[0])
	dark := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		return (r+g+b)/3 < 0xa000
	}
	for _, d := range dets {
		s := pages[0].Scale
		x0, y0 := int(float64(d.Box.X)*s), int(float64(d.Box.Y)*s)
		x1, y1 := int(float64(d.Box.X+d.Box.Width)*s), int(float64(d.Box.Y+d.Box.Height)*s)
		// Some pixel of the border's top edge must be ink in the preview.
		found := false
		for x := x0; x <= x1 && !found; x++ {
			for y := y0 - 1; y <= y0+1; y++ {
				if dark(x, y) {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("box %v scaled to (%d,%d)-(%d,%d) is on blank paper in the preview", d.Box, x0, y0, x1, y1)
		}
	}
}

// A page far bigger than letter, a survey plat, is drawn smaller rather than
// sent as a picture tens of megapixels big, and its scale says by how much.
func TestPreviewShrinksOutsizedPages(t *testing.T) {
	requireRasterizer(t)

	// 3000pt square is about 42 inches: 4167px a side at previewDPI.
	const side = 3000
	pdf := []byte("%PDF-1.4\n" +
		"1 0 obj <</Type/Catalog/Pages 2 0 R>> endobj\n" +
		"2 0 obj <</Type/Pages/Kids[3 0 R]/Count 1>> endobj\n" +
		"3 0 obj <</Type/Page/Parent 2 0 R/MediaBox[0 0 3000 3000]>> endobj\n" +
		"trailer <</Root 1 0 R>>\n%%EOF\n")
	pages := decodePreview(t, previewOf(t, "plat.pdf", pdf))
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	p := pages[0]
	jpegOf(t, p)
	if p.Width*p.Height > maxPreviewPixels {
		t.Errorf("preview is %dx%d, over maxPreviewPixels", p.Width, p.Height)
	}
	if p.Scale >= 1.0/3 {
		t.Errorf("scale = %v, want less than the usual 1/3", p.Scale)
	}
	// The scale has to describe the picture: the page at rasterDPI, scaled,
	// is the preview's width, give or take pdftoppm's rounding.
	if want := side * rasterDPI / pdfPoints * p.Scale; math.Abs(float64(p.Width)-want) > 2 {
		t.Errorf("width = %d, want about %.0f from the scale", p.Width, want)
	}
}

func TestPreviewStopsAtThePagesDetectionScans(t *testing.T) {
	requireRasterizer(t)

	in := make([]string, maxPDFPages+2)
	for i := range in {
		in[i] = "testdata/scanned-form.pdf"
	}
	path := t.TempDir() + "/long.pdf"
	if err := api.MergeCreateFile(in, path, false, nil); err != nil {
		t.Fatal(err)
	}
	pages := decodePreview(t, previewOf(t, "long.pdf", uploadFile(t, path)))
	if len(pages) != maxPDFPages {
		t.Errorf("got %d pages, want %d", len(pages), maxPDFPages)
	}
}

func TestPreviewRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		content  []byte
		want     int
	}{
		{"an image, which needs no preview", "form.png", uploadFile(t, "testdata/form.png"), http.StatusUnsupportedMediaType},
		{"a file that is not a document", "notes.txt", []byte("hello\n"), http.StatusUnsupportedMediaType},
		{"a pdf that cannot be read", "broken.pdf", []byte("%PDF-1.4\nnot really a pdf at all"), http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == http.StatusUnprocessableEntity {
				requireRasterizer(t)
			}
			rec := previewOf(t, tc.filename, tc.content)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.want, rec.Body)
			}
			errorMessage(t, rec)
		})
	}
}

func TestPreviewReportsMissingRasterizer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rec := previewOf(t, "scan.pdf", uploadFile(t, "testdata/scanned-form.pdf"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body %s", rec.Code, rec.Body)
	}
	errorMessage(t, rec)
}
