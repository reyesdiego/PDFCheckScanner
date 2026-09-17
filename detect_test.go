package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// upload builds a multipart body with one file field and posts it to the router.
func upload(t *testing.T, field, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/detect", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)
	return rec
}

func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noisyPNG builds a PNG of random pixels, which barely compresses, so the
// upload is large enough to spill out of memory during multipart parsing.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(rand.IntN(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sampleJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) detectResponse {
	t.Helper()
	var got detectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body, err)
	}
	return got
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body, err)
	}
	if got["error"] == "" {
		t.Error("no error message in response")
	}
	return got["error"]
}

func TestDetectAcceptsPNGUpload(t *testing.T) {
	content := samplePNG(t, 12, 7)
	rec := upload(t, imageField, "front-door.png", content)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	got := decodeResponse(t, rec)
	if got.Image.ContentType != "image/png" {
		t.Errorf("content_type = %q, want image/png", got.Image.ContentType)
	}
	if got.Image.Width != 12 || got.Image.Height != 7 {
		t.Errorf("dimensions = %dx%d, want 12x7", got.Image.Width, got.Image.Height)
	}
	if got.Image.SizeBytes != int64(len(content)) {
		t.Errorf("size_bytes = %d, want %d", got.Image.SizeBytes, len(content))
	}
	if got.Image.Filename != "front-door.png" {
		t.Errorf("filename = %q", got.Image.Filename)
	}
	if got.Detections == nil {
		t.Error("detections is null, want an empty array")
	}
	if got.RequestID == "" {
		t.Error("request_id is empty")
	}
}

func TestDetectAcceptsJPEGUpload(t *testing.T) {
	rec := upload(t, imageField, "kitchen.jpg", sampleJPEG(t, 8, 5))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decodeResponse(t, rec)
	if got.Image.ContentType != "image/jpeg" {
		t.Errorf("content_type = %q, want image/jpeg", got.Image.ContentType)
	}
	if got.Image.Width != 8 || got.Image.Height != 5 {
		t.Errorf("dimensions = %dx%d, want 8x5", got.Image.Width, got.Image.Height)
	}
}

func TestDetectAcceptsWebPUpload(t *testing.T) {
	cases := []struct {
		name          string
		file          string
		width, height int
	}{
		{"lossless", "testdata/lossless.webp", 75, 100},
		{"lossy", "testdata/lossy.webp", 150, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, err := os.ReadFile(c.file)
			if err != nil {
				t.Fatal(err)
			}
			rec := upload(t, imageField, "patio.webp", content)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
			}
			got := decodeResponse(t, rec)
			if got.Image.ContentType != "image/webp" {
				t.Errorf("content_type = %q, want image/webp", got.Image.ContentType)
			}
			if got.Image.Width != c.width || got.Image.Height != c.height {
				t.Errorf("dimensions = %dx%d, want %dx%d",
					got.Image.Width, got.Image.Height, c.width, c.height)
			}
			if got.Image.SizeBytes != int64(len(content)) {
				t.Errorf("size_bytes = %d, want %d", got.Image.SizeBytes, len(content))
			}
		})
	}
}

func TestDetectRejectsMalformedImage(t *testing.T) {
	// A real PNG signature followed by junk: it sniffs as image/png but no
	// header can be read out of it.
	content := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("junk"), 20)...)
	rec := upload(t, imageField, "broken.png", content)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %s)", rec.Code, rec.Body)
	}
	if msg := errorMessage(t, rec); !strings.Contains(msg, "malformed") {
		t.Errorf("error = %q", msg)
	}
}

// A client may lie about the part's Content-Type; the sniffed bytes decide.
func TestDetectIgnoresDeclaredPartContentType(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="image"; filename="evil.png"`}
	h["Content-Type"] = []string{"image/png"}
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("#!/bin/sh\nrm -rf /\n"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/detect", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %s)", rec.Code, rec.Body)
	}
	if msg := errorMessage(t, rec); !strings.Contains(msg, "unsupported type") {
		t.Errorf("error = %q", msg)
	}
}

func TestDetectStripsDirectoriesFromFilename(t *testing.T) {
	rec := upload(t, imageField, "../../etc/passwd.png", samplePNG(t, 2, 2))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := decodeResponse(t, rec).Image.Filename; got != "passwd.png" {
		t.Errorf("filename = %q, want passwd.png", got)
	}
}

func TestDetectRejectsEmptyUpload(t *testing.T) {
	rec := upload(t, imageField, "empty.png", nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %s)", rec.Code, rec.Body)
	}
	if msg := errorMessage(t, rec); !strings.Contains(msg, "empty") {
		t.Errorf("error = %q", msg)
	}
}

func TestDetectRejectsWrongFieldName(t *testing.T) {
	rec := upload(t, "photo", "front-door.png", samplePNG(t, 2, 2))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	errorMessage(t, rec)
}

func TestDetectRejectsNonMultipartBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/detect",
		strings.NewReader(`{"image_url":"https://example.com/house.jpg"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	if msg := errorMessage(t, rec); !strings.Contains(msg, "multipart/form-data") {
		t.Errorf("error = %q", msg)
	}
}

func TestDetectRejectsOversizedUpload(t *testing.T) {
	rec := upload(t, imageField, "huge.png", bytes.Repeat([]byte{0}, maxUploadBytes+1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body)
	}
	errorMessage(t, rec)
}

// Uploads larger than maxMemoryBytes spill to a temp file, which the handler
// must clean up before returning.
func TestDetectCleansUpSpilledTempFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	content := noisyPNG(t, 1100, 1100)
	if len(content) <= maxMemoryBytes {
		t.Fatalf("test image is %d bytes, need more than maxMemoryBytes (%d)", len(content), maxMemoryBytes)
	}
	if rec := upload(t, imageField, "big.png", content); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// The endpoint must report what the detector finds, not just echo the image.
func TestDetectEndpointReturnsCheckboxes(t *testing.T) {
	form := blankForm(220, 80)
	drawBox(form, 20, 20, 24, 1)
	drawBox(form, 90, 20, 24, 1)
	drawCross(form, 90, 20, 24)

	var content bytes.Buffer
	if err := png.Encode(&content, form); err != nil {
		t.Fatal(err)
	}

	rec := upload(t, imageField, "form.png", content.Bytes())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	got := decodeResponse(t, rec)
	if got.Image.Width != 220 || got.Image.Height != 80 {
		t.Errorf("dimensions = %dx%d, want 220x80", got.Image.Width, got.Image.Height)
	}
	if len(got.Detections) != 2 {
		t.Fatalf("got %d detections, want 2: %+v", len(got.Detections), got.Detections)
	}
	if got.Detections[0].Checked || !got.Detections[1].Checked {
		t.Errorf("checked = [%v %v], want [false true]",
			got.Detections[0].Checked, got.Detections[1].Checked)
	}
	if b := got.Detections[1].Box; b.X != 90 || b.Y != 20 || b.Width != 24 || b.Height != 24 {
		t.Errorf("second box = %+v, want {90 20 24 24}", b)
	}
	if got.Detections[0].Label != "checkbox" {
		t.Errorf("label = %q, want checkbox", got.Detections[0].Label)
	}
}

func TestDetectRejectsTooManyPixels(t *testing.T) {
	// A large but highly compressible image: small on the wire, over the
	// pixel budget once decoded.
	side := 5000
	if side*side <= maxPixels {
		t.Fatalf("test image is %d pixels, need more than maxPixels (%d)", side*side, maxPixels)
	}
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewGray(image.Rect(0, 0, side, side))); err != nil {
		t.Fatal(err)
	}
	if content.Len() > maxUploadBytes {
		t.Fatalf("encoded image is %d bytes, over the upload cap", content.Len())
	}

	rec := upload(t, imageField, "huge.png", content.Bytes())
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body)
	}
	if msg := errorMessage(t, rec); !strings.Contains(msg, "pixels") {
		t.Errorf("error = %q", msg)
	}
}

func TestDetectEndpointReadsPDFFormFields(t *testing.T) {
	content := uploadFile(t, "testdata/form-fields.pdf")
	rec := upload(t, imageField, "application.pdf", content)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	got := decodeResponse(t, rec)
	if got.Image.ContentType != "application/pdf" {
		t.Errorf("content_type = %q, want application/pdf", got.Image.ContentType)
	}
	if got.Image.Pages != 3 {
		t.Errorf("pages = %d, want 3", got.Image.Pages)
	}
	if got.Method != methodAcroForm {
		t.Errorf("method = %q, want %q", got.Method, methodAcroForm)
	}
	if got.Image.Width != 0 || got.Image.Height != 0 {
		t.Errorf("width/height = %dx%d, want them omitted for a pdf",
			got.Image.Width, got.Image.Height)
	}
	if len(got.Detections) != 4 {
		t.Fatalf("got %d detections, want 4: %+v", len(got.Detections), got.Detections)
	}
	if !got.Detections[0].Checked || got.Detections[0].Page != 1 {
		t.Errorf("first = %+v, want checked on page 1", got.Detections[0])
	}
	if got.Detections[0].Name != "agree" {
		t.Errorf("name = %q, want agree", got.Detections[0].Name)
	}
}

func TestDetectEndpointRastersScannedPDF(t *testing.T) {
	requireRasterizer(t)

	content := uploadFile(t, "testdata/scanned-form.pdf")
	rec := upload(t, imageField, "scan.pdf", content)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	got := decodeResponse(t, rec)
	if got.Method != methodPixels {
		t.Errorf("method = %q, want %q", got.Method, methodPixels)
	}
	if got.Image.Pages != 1 {
		t.Errorf("pages = %d, want 1", got.Image.Pages)
	}
	if len(got.Detections) != 8 {
		t.Fatalf("got %d detections, want 8", len(got.Detections))
	}
	checked := 0
	for _, d := range got.Detections {
		if d.Page != 1 {
			t.Errorf("page = %d, want 1", d.Page)
		}
		if d.Checked {
			checked++
		}
	}
	if checked != 3 {
		t.Errorf("%d checked, want 3", checked)
	}
}

// Image uploads must not grow a page field now that PDFs have one.
func TestDetectEndpointOmitsPageForImages(t *testing.T) {
	form := blankForm(120, 80)
	drawBox(form, 20, 20, 24, 1)
	var content bytes.Buffer
	if err := png.Encode(&content, form); err != nil {
		t.Fatal(err)
	}

	rec := upload(t, imageField, "one.png", content.Bytes())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, `"page"`) ||
		strings.Contains(body, `"pages"`) || strings.Contains(body, `"name"`) {
		t.Errorf("image response mentions pages or field names: %s", body)
	}
	if got := decodeResponse(t, rec); got.Method != methodPixels {
		t.Errorf("method = %q, want %q", got.Method, methodPixels)
	}
}

func TestDetectEndpointRejectsCorruptPDF(t *testing.T) {
	rec := upload(t, imageField, "broken.pdf", []byte("%PDF-1.4\nnot really a pdf at all"))
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 422 (or 503 without a rasterizer), body %s", rec.Code, rec.Body)
	}
	errorMessage(t, rec)
}

func TestOnlyDetectIsRouted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/detect", nil)
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /detect: status = %d, want 405", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /: status = %d, want 404", rec.Code)
	}
}
