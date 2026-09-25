package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// uploadFile reads a fixture, failing the test before any use of the content
// if the file is missing or empty.
func uploadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	if len(content) == 0 {
		t.Fatalf("fixture %s is empty", path)
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
	if want := (box{X: 833, Y: 1142, Width: 75, Height: 75}); nested.Box != want {
		t.Errorf("box = %+v, want %+v", nested.Box, want)
	}
}

// Rect[100 700 118 718] on a 612x792 page is 18pt square, 100pt from the left
// and 74pt below the top, which at 300 DPI is a 75px box at (417,308).
func TestAcroFormRectBecomesPixels(t *testing.T) {
	dets, _, _, err := detectPDF(context.Background(), "testdata/form-fields.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got := dets[0].Box
	if got.X != 417 || got.Y != 308 || got.Width != 75 || got.Height != 75 {
		t.Errorf("box = %+v, want {417 308 75 75}", got)
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
	if got.X != 2917 || got.Y != 417 || got.Width != 75 || got.Height != 75 {
		t.Errorf("rotated box = %+v, want {2917 417 75 75}", got)
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
		{0, box{X: 417, Y: 308, Width: 75, Height: 75}},
		{90, box{X: 2917, Y: 417, Width: 75, Height: 75}},
		{180, box{X: 2058, Y: 2917, Width: 75, Height: 75}},
		{270, box{X: 308, Y: 2058, Width: 75, Height: 75}},
		{-90, box{X: 308, Y: 2058, Width: 75, Height: 75}},
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
	if want := (box{X: 417, Y: 308, Width: 75, Height: 75}); got != want {
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

// A checkbox field that cannot be placed must not be dropped from an answer
// that claims to be exact: the whole document goes to the pixels instead,
// where all eight printed boxes are found.
func TestDetectPDFFallsBackWhenACheckboxIsDamaged(t *testing.T) {
	requireRasterizer(t)

	const path = "testdata/scanned-damaged-checkbox.pdf"
	_, _, err := acroFormCheckboxes(path)
	if err == nil {
		t.Fatal("the fields read cleanly; one checkbox is meant to be unplaceable")
	}
	if !strings.Contains(err.Error(), `"damaged"`) {
		t.Errorf("error %q does not name the damaged checkbox", err)
	}

	dets, _, method, err := detectPDF(context.Background(), path)
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels {
		t.Errorf("method = %q, want %q", method, methodPixels)
	}
	if len(dets) != 8 {
		t.Errorf("found %d checkboxes, want 8", len(dets))
	}
}

// oversizePDF writes a one-page PDF whose MediaBox is large enough that the
// page exceeds maxPixels when rendered at rasterDPI, with checkboxes drawn on
// it at the given points.
func oversizePDF(t *testing.T, side float64, at [][2]float64) string {
	t.Helper()

	var content strings.Builder
	content.WriteString("2 w 0 G\n")
	for _, p := range at {
		fmt.Fprintf(&content, "%.0f %.0f %.0f %.0f re S\n", p[0], p[1], side, side)
	}

	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 %d %d]/Contents 4 0 R/Resources<<>>>>",
			oversizePoints, oversizePoints),
		fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", content.Len(), content.String()),
	}

	var out strings.Builder
	out.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)

	path := t.TempDir() + "/oversize.pdf"
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// oversizePoints is a page side, in PDF points, that rasterizes past
// maxPixels at rasterDPI.
const oversizePoints = 1250

// A page too big to rasterize at full resolution must still be read, not
// silently contributed nothing, and its boxes must come back in the same
// coordinate space as every other page: pixels at rasterDPI.
func TestDetectPDFReadsPagesTooLargeToRasterFully(t *testing.T) {
	requireRasterizer(t)
	if testing.Short() {
		t.Skip("renders a page of tens of megapixels, twice")
	}
	if px := px(oversizePoints) * px(oversizePoints); px <= maxPixels {
		t.Fatalf("fixture page is %d pixels at %d DPI, need more than maxPixels (%d)",
			px, rasterDPI, maxPixels)
	}

	// Drawn top row first, so the list is already in reading order: the PDF
	// origin is bottom-left, so the higher y is the upper box.
	const side = 30.0
	at := [][2]float64{{200, 900}, {600, 900}, {200, 800}}
	dets, _, method, err := detectPDF(context.Background(), oversizePDF(t, side, at))
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels {
		t.Errorf("method = %q, want %q", method, methodPixels)
	}
	if len(dets) != len(at) {
		t.Fatalf("found %d checkboxes, want %d: %+v", len(dets), len(at), dets)
	}

	// Coordinates are reported at rasterDPI even though the page was rendered
	// smaller, so the boxes land where the PDF put them. A stroke straddles
	// its path, so the drawn box is a stroke wider than the rectangle and
	// starts half a stroke outside it.
	const stroke, tol = 2.0, 6
	for i, d := range dets {
		wantW := px(side + stroke)
		wantX, wantY := px(at[i][0]-stroke/2), px(oversizePoints-at[i][1]-side-stroke/2)
		if abs(d.Box.Width-wantW) > tol || abs(d.Box.Height-wantW) > tol {
			t.Errorf("[%d] size = %dx%d, want ~%dx%d", i, d.Box.Width, d.Box.Height, wantW, wantW)
		}
		if abs(d.Box.X-wantX) > tol || abs(d.Box.Y-wantY) > tol {
			t.Errorf("[%d] origin = (%d,%d), want ~(%d,%d)", i, d.Box.X, d.Box.Y, wantX, wantY)
		}
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

// prose.pdf carries real Helvetica text and not one checkbox. It is the
// fixture the suite was missing: every generated fixture before it was boxes
// on blank paper, so the tests all passed while the detector was reporting
// 167 checkboxes on a page of prose.
func TestDetectPDFIgnoresProse(t *testing.T) {
	requireRasterizer(t)

	dets, pages, method, err := detectPDF(context.Background(), "testdata/prose.pdf")
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels || pages != 1 {
		t.Fatalf("method %q over %d pages, want %q over 1", method, pages, methodPixels)
	}
	if len(dets) != 0 {
		t.Errorf("found %d checkboxes on a page of text: %+v", len(dets), dets)
	}
}

// mixed.pdf is text and checkboxes on one page, which is what a form actually
// looks like, so it exercises precision and recall at the same time.
func TestDetectPDFFindsBoxesAmongText(t *testing.T) {
	requireRasterizer(t)

	dets, _, _, err := detectPDF(context.Background(), "testdata/mixed.pdf")
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}

	want := []bool{false, true, true, false, false, true}
	if len(dets) != len(want) {
		t.Fatalf("found %d checkboxes, want %d: %+v", len(dets), len(want), dets)
	}
	for i, d := range dets {
		if d.Checked != want[i] {
			t.Errorf("[%d] at (%d,%d) checked = %v, want %v",
				i, d.Box.X, d.Box.Y, d.Checked, want[i])
		}
	}
}

// Only pdftoppm refusing the file makes it the client's fault; the handler
// turns that into a 422 and everything else into a 500.
func TestDetectPDFSeparatesBadInputFromServerFailure(t *testing.T) {
	requireRasterizer(t)

	garbage := t.TempDir() + "/broken.pdf"
	if err := os.WriteFile(garbage, []byte("%PDF-1.4\nnot really a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := detectPDF(context.Background(), garbage); !errors.Is(err, errUnreadablePDF) {
		t.Errorf("garbage: err = %v, want errUnreadablePDF", err)
	}

	t.Setenv("TMPDIR", t.TempDir()+"/missing")
	_, _, _, err := detectPDF(context.Background(), "testdata/scanned-form.pdf")
	if err == nil {
		t.Fatal("no temp dir: detectPDF succeeded, want error")
	}
	if errors.Is(err, errUnreadablePDF) {
		t.Errorf("no temp dir: err = %v, blames the pdf", err)
	}
}

func TestDetectPDFScansOnlyTheFirstPages(t *testing.T) {
	requireRasterizer(t)

	const total = maxPDFPages + 2
	in := make([]string, total)
	for i := range in {
		in[i] = "testdata/scanned-form.pdf"
	}
	path := t.TempDir() + "/long.pdf"
	if err := api.MergeCreateFile(in, path, false, nil); err != nil {
		t.Fatal(err)
	}

	dets, pages, _, err := detectPDF(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if pages != total {
		t.Errorf("pages = %d, want the true total %d", pages, total)
	}
	perPage := make(map[int]int)
	for _, d := range dets {
		perPage[d.Page]++
	}
	for page := 1; page <= total; page++ {
		want := 8
		if page > maxPDFPages {
			want = 0
		}
		if perPage[page] != want {
			t.Errorf("page %d: %d boxes, want %d", page, perPage[page], want)
		}
	}
}
