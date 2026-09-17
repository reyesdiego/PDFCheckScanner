package main

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func uploadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func requireRasterizer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(rasterizer); err != nil {
		t.Skipf("%s not installed", rasterizer)
	}
}

// The fast path must read the PDF's own answers and ignore every field that is
// not a checkbox.
func TestDetectPDFUsesAcroFormFields(t *testing.T) {
	dets, pages, method, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodAcroForm {
		t.Errorf("method = %q, want %q", method, methodAcroForm)
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
	if len(dets) != 4 {
		t.Fatalf("found %d checkboxes, want 4 (radio, push button and text field excluded): %+v",
			len(dets), dets)
	}

	for i, d := range dets {
		if d.Label != "checkbox" {
			t.Errorf("[%d] label = %q", i, d.Label)
		}
		// The document states its own answer, so there is nothing to estimate.
		if d.Confidence != 1 {
			t.Errorf("[%d] confidence = %v, want 1", i, d.Confidence)
		}
	}

	// Page 1 holds the checked "agree" and the unchecked "subscribe".
	if dets[0].Page != 1 || !dets[0].Checked {
		t.Errorf("first = page %d checked %v, want page 1 checked true", dets[0].Page, dets[0].Checked)
	}
	if dets[1].Page != 1 || dets[1].Checked {
		t.Errorf("second = page %d checked %v, want page 1 checked false", dets[1].Page, dets[1].Checked)
	}
	if dets[2].Page != 2 || !dets[2].Checked {
		t.Errorf("third = page %d checked %v, want page 2 checked true", dets[2].Page, dets[2].Checked)
	}
	if dets[3].Page != 3 || !dets[3].Checked {
		t.Errorf("fourth = page %d checked %v, want page 3 checked true", dets[3].Page, dets[3].Checked)
	}
}

// The field name is the most useful thing an AcroForm gives you: it says which
// question each answer belongs to.
func TestAcroFormReportsFieldNames(t *testing.T) {
	dets, _, _, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, d := range dets {
		got = append(got, d.Name)
	}
	// "consent.marketing" is a widget nested under a parent field, so its name
	// is qualified with the parent's.
	want := []string{"agree", "subscribe", "consent.marketing", "third"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] name = %q, want %q", i, got[i], want[i])
		}
	}
}

// A nested widget inherits its field type and value from the parent field, so
// dropping the /Parent walk would lose this one entirely.
func TestAcroFormResolvesInheritedFields(t *testing.T) {
	dets, _, _, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatal(err)
	}
	nested := dets[2]
	if nested.Name != "consent.marketing" {
		t.Fatalf("name = %q, want consent.marketing", nested.Name)
	}
	if !nested.Checked {
		t.Error("checked = false, want true (inherited /V with /AS On)")
	}
	if want := (box{X: 556, Y: 761, Width: 50, Height: 50}); nested.Box != want {
		t.Errorf("box = %+v, want %+v", nested.Box, want)
	}
}

// Rect[100 700 118 718] on a 612x792 page is 18pt square, 100pt from the left
// and 74pt below the top, which at 200 DPI is a 50px box at (278,206).
func TestAcroFormRectBecomesPixels(t *testing.T) {
	dets, _, _, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got := dets[0].Box
	if got.X != 278 || got.Y != 206 || got.Width != 50 || got.Height != 50 {
		t.Errorf("box = %+v, want {278 206 50 50}", got)
	}
}

// pdftoppm renders a /Rotate 90 page turned, so the reported box has to turn
// with it or it will not line up with the image a caller draws on.
func TestAcroFormHonoursPageRotation(t *testing.T) {
	dets, _, _, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatal(err)
	}
	// Same rect as page 1, on a page rotated 90 degrees clockwise.
	got := dets[3].Box
	if got.X != 1944 || got.Y != 278 || got.Width != 50 || got.Height != 50 {
		t.Errorf("rotated box = %+v, want {1944 278 50 50}", got)
	}
}

func TestRectToPixelsRotations(t *testing.T) {
	page := func(rotate int) *model.InheritedPageAttrs {
		return &model.InheritedPageAttrs{
			MediaBox: types.NewRectangle(0, 0, 612, 792),
			Rotate:   rotate,
		}
	}
	rect := types.NewRectangle(100, 700, 118, 718)

	tests := []struct {
		rotate int
		want   box
	}{
		{0, box{X: 278, Y: 206, Width: 50, Height: 50}},
		{90, box{X: 1944, Y: 278, Width: 50, Height: 50}},
		{180, box{X: 1372, Y: 1944, Width: 50, Height: 50}},
		{270, box{X: 206, Y: 1372, Width: 50, Height: 50}},
		{-90, box{X: 206, Y: 1372, Width: 50, Height: 50}},
	}
	for _, tc := range tests {
		if got := rectToPixels(rect, page(tc.rotate)); got != tc.want {
			t.Errorf("rotate %d: box = %+v, want %+v", tc.rotate, got, tc.want)
		}
	}
}

// A MediaBox that does not start at the origin must not shift every box.
func TestRectToPixelsHonoursMediaBoxOrigin(t *testing.T) {
	attrs := &model.InheritedPageAttrs{MediaBox: types.NewRectangle(20, 30, 632, 822)}
	got := rectToPixels(types.NewRectangle(120, 730, 138, 748), attrs)
	if want := (box{X: 278, Y: 206, Width: 50, Height: 50}); got != want {
		t.Errorf("box = %+v, want %+v", got, want)
	}
}

// A flattened page has no fields, so it has to go through the rasterizer.
func TestDetectPDFFallsBackToRaster(t *testing.T) {
	requireRasterizer(t)

	dets, pages, method, err := detectPDF(context.Background(), "testdata/scanned-form.pdf")
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels {
		t.Errorf("method = %q, want %q", method, methodPixels)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	if len(dets) != 8 {
		t.Fatalf("found %d checkboxes, want 8: %+v", len(dets), dets)
	}

	var checked []int
	for i, d := range dets {
		if d.Page != 1 {
			t.Errorf("[%d] page = %d, want 1", i, d.Page)
		}
		if d.Confidence <= 0 || d.Confidence > 1 {
			t.Errorf("[%d] confidence = %v, want (0,1]", i, d.Confidence)
		}
		if d.Checked {
			checked = append(checked, i)
		}
	}
	// The fixture marks the first, fourth and sixth box, top to bottom.
	if want := []int{0, 3, 5}; !equalInts(checked, want) {
		t.Errorf("checked boxes = %v, want %v", checked, want)
	}
}

// pdfcpu rejects this file's form fields as invalid, but poppler renders the
// page fine, so the request must still come back with the checkboxes rather
// than an error.
func TestDetectPDFFallsBackWhenFieldsAreUnreadable(t *testing.T) {
	requireRasterizer(t)

	if _, _, err := acroFormCheckboxes("testdata/scanned-broken-fields.pdf"); err == nil {
		t.Fatal("the fixture parsed cleanly; it is meant to fail the fast path")
	}

	dets, pages, method, err := detectPDF(context.Background(), "testdata/scanned-broken-fields.pdf")
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels {
		t.Errorf("method = %q, want %q", method, methodPixels)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	if len(dets) != 8 {
		t.Errorf("found %d checkboxes, want 8", len(dets))
	}
}

func TestDetectPDFRejectsGarbage(t *testing.T) {
	path := t.TempDir() + "/broken.pdf"
	if err := os.WriteFile(path, []byte("%PDF-1.4\nnot really a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := detectPDF(context.Background(), path); err == nil {
		t.Error("detectPDF succeeded, want error")
	}
}

func TestDetectPDFHonoursCanceledContext(t *testing.T) {
	requireRasterizer(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := detectPDF(ctx, "testdata/scanned-form.pdf"); err == nil {
		t.Error("detectPDF succeeded, want a context error")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
