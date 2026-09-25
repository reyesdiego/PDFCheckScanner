package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"

	"gocv.io/x/gocv"
	"testing"
)

// blankForm returns a white canvas to draw test forms on.
func blankForm(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func ink(img *image.Gray, x, y int) {
	if (image.Point{X: x, Y: y}).In(img.Bounds()) {
		img.SetGray(x, y, color.Gray{})
	}
}

// drawBox draws a hollow square with the given stroke thickness, as four
// filled bands that do not overlap, so every border pixel is written once.
func drawBox(img *image.Gray, x, y, side, stroke int) {
	for _, band := range []image.Rectangle{
		image.Rect(x, y, x+side, y+stroke),                         // top
		image.Rect(x, y+side-stroke, x+side, y+side),               // bottom
		image.Rect(x, y+stroke, x+stroke, y+side-stroke),           // left
		image.Rect(x+side-stroke, y+stroke, x+side, y+side-stroke), // right
	} {
		fillInk(img, band)
	}
}

// fillInk inks every pixel of r that lies inside img, a row at a time.
func fillInk(img *image.Gray, r image.Rectangle) {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.PixOffset(r.Min.X, y)
		clear(img.Pix[row : row+r.Dx()])
	}
}

// line draws a thick line between two points.
func line(img *image.Gray, x0, y0, x1, y1, thickness int) {
	steps := max(abs(x1-x0), abs(y1-y0))
	for s := 0; s <= steps; s++ {
		x := x0 + (x1-x0)*s/steps
		y := y0 + (y1-y0)*s/steps
		for dy := range thickness {
			for dx := range thickness {
				ink(img, x+dx, y+dy)
			}
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// drawCross marks a box with an X that stays clear of its border.
func drawCross(img *image.Gray, x, y, side int) {
	in := side / 4
	line(img, x+in, y+in, x+side-in, y+side-in, 2)
	line(img, x+side-in, y+in, x+in, y+side-in, 2)
}

// drawTick marks a box with a check mark.
func drawTick(img *image.Gray, x, y, side int) {
	line(img, x+side/4, y+side/2, x+side/2, y+side-side/4, 2)
	line(img, x+side/2, y+side-side/4, x+side-side/5, y+side/5, 2)
}

func fillRect(img *image.Gray, x, y, w, h int) {
	for dy := range h {
		for dx := range w {
			ink(img, x+dx, y+dy)
		}
	}
}

func wantBox(t *testing.T, got box, x, y, w, h int) {
	t.Helper()
	if got.X != x || got.Y != y || got.Width != w || got.Height != h {
		t.Errorf("box = {%d %d %d %d}, want {%d %d %d %d}",
			got.X, got.Y, got.Width, got.Height, x, y, w, h)
	}
}

func TestDetectReportsCheckedState(t *testing.T) {
	img := blankForm(220, 80)
	drawBox(img, 20, 20, 24, 1) // empty
	drawBox(img, 90, 20, 24, 1) // crossed
	drawCross(img, 90, 20, 24)
	drawBox(img, 160, 20, 24, 1) // ticked
	drawTick(img, 160, 20, 24)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 3 {
		t.Fatalf("found %d checkboxes, want 3: %+v", len(got), got)
	}

	wantChecked := []bool{false, true, true}
	wantX := []int{20, 90, 160}
	for i, d := range got {
		if d.Checked != wantChecked[i] {
			t.Errorf("[%d] checked = %v, want %v (box %+v)", i, d.Checked, wantChecked[i], d.Box)
		}
		wantBox(t, d.Box, wantX[i], 20, 24, 24)
		if d.Confidence <= 0 || d.Confidence > 1 {
			t.Errorf("[%d] confidence = %v, want (0,1]", i, d.Confidence)
		}
	}
}

// A mark drawn over the border merges with it into one component, which must
// still be recognised.
func TestDetectHandlesMarkTouchingBorder(t *testing.T) {
	img := blankForm(80, 80)
	drawBox(img, 20, 20, 30, 2)
	line(img, 20, 20, 49, 49, 2)
	line(img, 49, 20, 20, 49, 2)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	if !got[0].Checked {
		t.Error("checked = false, want true")
	}
}

func TestDetectThickBorderAndLargeBox(t *testing.T) {
	img := blankForm(200, 200)
	drawBox(img, 40, 40, 90, 4)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	if got[0].Checked {
		t.Error("checked = true, want false")
	}
	wantBox(t, got[0].Box, 40, 40, 90, 90)
}

// mustDetect runs the detector and fails the test if it could not examine
// the image, so that a broken detector never reads as an empty page.
func mustDetect(tb testing.TB, d *Detector, img image.Image) []detection {
	tb.Helper()
	got, err := d.Detect(img)
	if err != nil {
		tb.Fatalf("Detect: %v", err)
	}
	return got
}

// loadPNG reads a committed PNG fixture.
func loadPNG(tb testing.TB, path string) image.Image {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		tb.Fatal(err)
	}
	return img
}

// blur softens an image with a 3x3 box filter, so a drawn box picks up the
// grey fringe a scan would give it.
func blur(img *image.Gray) *image.Gray {
	out := image.NewGray(img.Bounds())
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			sum, n := 0, 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if p := (image.Point{X: x + dx, Y: y + dy}); p.In(b) {
						sum += int(img.GrayAt(p.X, p.Y).Y)
						n++
					}
				}
			}
			out.SetGray(x, y, color.Gray{Y: uint8(sum / n)})
		}
	}
	return out
}

// strokeWidth has to find the border even when the candidate's rectangle
// does not sit exactly on it, because a softly binarized contour lands a
// pixel or two outside. The budget it scans with counts stroke, not steps:
// charging the skipped background against it left a 3px border on a 16px box
// measuring 2, which is enough to leave a row of border inside the interior
// window and read an empty box as marked.
func TestStrokeWidth(t *testing.T) {
	// An ink mask, drawn by hand so the border is exactly where it says.
	const w, h, stroke = 60, 60, 3
	pix := make([]byte, w*h)
	drawInkBox := func(x, y, side int) {
		for t := range stroke {
			for i := range side {
				pix[(y+t)*w+x+i] = 255
				pix[(y+side-1-t)*w+x+i] = 255
				pix[(y+i)*w+x+t] = 255
				pix[(y+i)*w+x+side-1-t] = 255
			}
		}
	}
	drawInkBox(4, 4, 12) // small enough that the scan budget bites
	drawInkBox(30, 30, 20)

	mask, err := gocv.NewMatFromBytes(h, w, gocv.MatTypeCV8U, pix)
	if err != nil {
		t.Fatal(err)
	}
	defer mask.Close()

	small := image.Rect(4, 4, 16, 16)
	large := image.Rect(30, 30, 50, 50)
	for _, c := range []struct {
		name string
		r    image.Rectangle
		want int
	}{
		{"on the border", small, stroke},
		{"a pixel outside", small.Inset(-1), stroke},
		// The case the budget used to get wrong: two pixels of lead plus a
		// 3px border is five steps, and a 20px rect allows only five, so any
		// smaller box came up short.
		{"two pixels outside", small.Inset(-2), stroke},
		{"further out than a contour ever lands", small.Inset(-4), 0},
		{"a larger box", large, stroke},
		{"nowhere near ink", image.Rect(22, 4, 30, 12), 0},
	} {
		if got := strokeWidth(mask, c.r); got != c.want {
			t.Errorf("%s: strokeWidth = %d, want %d", c.name, got, c.want)
		}
	}
}

// A small box's border is a large fraction of its side, so a measurement
// window inset by a fixed fraction still contains the stroke and reads the
// border as if it were a mark.
func TestDetectSmallBoxWithThickBorderIsNotChecked(t *testing.T) {
	for _, side := range []int{14, 16, 20} {
		img := blankForm(60, 60)
		drawBox(img, 20, 20, side, 3)

		got := mustDetect(t, NewDetector(), blur(img))
		if len(got) != 1 {
			t.Fatalf("side %d: found %d checkboxes, want 1: %+v", side, len(got), got)
		}
		if got[0].Checked {
			t.Errorf("side %d: checked = true, want false", side)
		}
	}
}

// The same small thick-bordered box still has to register a mark.
func TestDetectSmallBoxWithThickBorderSeesMark(t *testing.T) {
	img := blankForm(60, 60)
	drawBox(img, 20, 20, 16, 3)
	line(img, 23, 23, 32, 32, 2)
	line(img, 32, 23, 23, 32, 2)

	got := mustDetect(t, NewDetector(), blur(img))
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	if !got[0].Checked {
		t.Error("checked = false, want true")
	}
}

func TestDetectIgnoresNonCheckboxes(t *testing.T) {
	tests := []struct {
		name string
		draw func(img *image.Gray)
	}{
		{"solid square", func(img *image.Gray) { fillRect(img, 20, 20, 30, 30) }},
		{"hollow circle", func(img *image.Gray) {
			const cx, cy, r = 45, 45, 20
			for deg := range 720 {
				rad := float64(deg) * math.Pi / 360
				x := cx + int(math.Round(r*math.Cos(rad)))
				y := cy + int(math.Round(r*math.Sin(rad)))
				ink(img, x, y)
				ink(img, x+1, y)
				ink(img, x, y+1)
			}
		}},
		{"letter H", func(img *image.Gray) {
			line(img, 20, 20, 20, 50, 2)
			line(img, 45, 20, 45, 50, 2)
			line(img, 20, 35, 45, 35, 2)
		}},
		{"horizontal rule", func(img *image.Gray) { fillRect(img, 10, 40, 70, 2) }},
		{"too small", func(img *image.Gray) { drawBox(img, 20, 20, 5, 1) }},
		{"blank", func(img *image.Gray) {}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img := blankForm(90, 90)
			tc.draw(img)
			if got := mustDetect(t, NewDetector(), img); len(got) != 0 {
				t.Errorf("found %d checkboxes, want 0: %+v", len(got), got)
			}
		})
	}
}

// A box ruled twice, or a box drawn inside a cell, must not be reported twice.
func TestDetectDedupesNestedBoxes(t *testing.T) {
	img := blankForm(100, 100)
	drawBox(img, 20, 20, 40, 1)
	drawBox(img, 24, 24, 32, 1)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
}

// A checkbox scanned at a high DPI is a physically normal checkbox, so the
// size cap has to grow with the image rather than throw the page away.
func TestDetectFindsLargeBoxOnHighResolutionPage(t *testing.T) {
	const side = 133 // over MaxSide, under the cap this page's size allows
	img := blankForm(2400, 3000)
	drawBox(img, 300, 400, side, 3)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	wantBox(t, got[0].Box, 300, 400, side, side)
}

// Containment is not duplication: a ruled cell encloses the checkboxes drawn
// in it, and dedupe keeps the larger candidate, so treating every enclosed
// box as a duplicate would delete the real answers.
func TestDetectKeepsBoxesInsideARuledCell(t *testing.T) {
	img := blankForm(200, 200)
	drawBox(img, 40, 40, 100, 2) // the cell
	drawBox(img, 55, 55, 20, 2)
	drawBox(img, 55, 95, 20, 2)
	line(img, 58, 98, 71, 111, 2) // mark the second one

	var inner []detection
	for _, d := range mustDetect(t, NewDetector(), img) {
		if d.Box.Width < 40 {
			inner = append(inner, d)
		}
	}
	if len(inner) != 2 {
		t.Fatalf("found %d boxes inside the cell, want 2: %+v", len(inner), inner)
	}
	wantBox(t, inner[0].Box, 55, 55, 20, 20)
	wantBox(t, inner[1].Box, 55, 95, 20, 20)
	if inner[0].Checked {
		t.Error("first box checked = true, want false")
	}
	if !inner[1].Checked {
		t.Error("second box checked = false, want true")
	}
}

func TestDetectSortsInReadingOrder(t *testing.T) {
	img := blankForm(200, 200)
	// Drawn out of order, and with a few pixels of row skew.
	drawBox(img, 120, 122, 24, 1)
	drawBox(img, 30, 30, 24, 1)
	drawBox(img, 120, 28, 24, 1)
	drawBox(img, 28, 120, 24, 1)

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 4 {
		t.Fatalf("found %d checkboxes, want 4: %+v", len(got), got)
	}
	wantOrder := [][2]int{{30, 30}, {120, 28}, {28, 120}, {120, 122}}
	for i, want := range wantOrder {
		if got[i].Box.X != want[0] || got[i].Box.Y != want[1] {
			t.Errorf("[%d] at (%d,%d), want (%d,%d)",
				i, got[i].Box.X, got[i].Box.Y, want[0], want[1])
		}
	}
}

// Photographed forms are never pure black on pure white.
func TestDetectToleratesNoiseAndGreyLevels(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	img := blankForm(160, 80)
	for i := range img.Pix {
		img.Pix[i] = uint8(200 + rng.IntN(56))
	}
	drawBox(img, 20, 20, 28, 2)
	drawBox(img, 90, 20, 28, 2)
	drawCross(img, 90, 20, 28)
	for i, v := range img.Pix {
		if v < 100 {
			img.Pix[i] = uint8(rng.IntN(70))
		}
	}

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 2 {
		t.Fatalf("found %d checkboxes, want 2: %+v", len(got), got)
	}
	if got[0].Checked || !got[1].Checked {
		t.Errorf("checked = [%v %v], want [false true]", got[0].Checked, got[1].Checked)
	}
}

// A transparent background must read as paper, not as ink.
func TestDetectHandlesTransparentBackground(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 20; y < 50; y++ {
		for x := 20; x < 50; x++ {
			onBorder := x == 20 || x == 49 || y == 20 || y == 49
			if onBorder {
				img.Set(x, y, color.RGBA{A: 255})
			}
		}
	}

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	if got[0].Checked {
		t.Error("checked = true, want false")
	}
}

// Mostly-ink images break the dark-on-light assumption and are skipped rather
// than guessed at.
func TestDetectSkipsMostlyInkImages(t *testing.T) {
	img := blankForm(80, 80)
	fillRect(img, 0, 0, 80, 70)
	if got := mustDetect(t, NewDetector(), img); len(got) != 0 {
		t.Errorf("found %d checkboxes, want 0: %+v", len(got), got)
	}
}

func TestDetectReturnsEmptySliceNotNil(t *testing.T) {
	if got := mustDetect(t, NewDetector(), blankForm(40, 40)); got == nil {
		t.Error("Detect returned nil, want an empty slice")
	}
}

func TestDetectHandlesTinyImage(t *testing.T) {
	if got := mustDetect(t, NewDetector(), blankForm(2, 2)); len(got) != 0 {
		t.Errorf("found %d checkboxes, want 0", len(got))
	}
}

// Regression: a page where ink covers a fraction of a percent. A global
// threshold picks a cut inside the paper noise here and loses every box.
func TestDetectFindsSparseBoxesOnNoisyPage(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	const w, h = 1600, 1200
	img := blankForm(w, h)
	for i := range img.Pix {
		img.Pix[i] = uint8(205 + rng.IntN(51))
	}

	var wantChecked int
	for row := range 4 {
		for col := range 4 {
			x, y := 200+col*340, 150+row*260
			drawBox(img, x, y, 34, 2)
			if (row+col)%3 == 0 {
				drawCross(img, x, y, 34)
				wantChecked++
			}
		}
	}
	for i, v := range img.Pix {
		if v < 100 {
			img.Pix[i] = uint8(rng.IntN(60))
		}
	}

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 16 {
		t.Fatalf("found %d checkboxes, want 16", len(got))
	}
	checked := 0
	for _, d := range got {
		if d.Checked {
			checked++
		}
	}
	if checked != wantChecked {
		t.Errorf("%d checked, want %d", checked, wantChecked)
	}
}

// Photographs of paper are lit unevenly; the far corner can be darker than the
// ink in the bright corner.
func TestDetectHandlesUnevenLighting(t *testing.T) {
	const w, h = 600, 400
	img := blankForm(w, h)
	for y := range h {
		for x := range w {
			shade := 255 - (x*90/w + y*60/h)
			img.Pix[y*w+x] = uint8(shade)
		}
	}
	for col := range 3 {
		x := 80 + col*180
		drawBox(img, x, 180, 30, 2)
		if col == 1 {
			drawTick(img, x, 180, 30)
		}
	}

	got := mustDetect(t, NewDetector(), img)
	if len(got) != 3 {
		t.Fatalf("found %d checkboxes, want 3: %+v", len(got), got)
	}
	if got[0].Checked || !got[1].Checked || got[2].Checked {
		t.Errorf("checked = [%v %v %v], want [false true false]",
			got[0].Checked, got[1].Checked, got[2].Checked)
	}
}

func TestBinarizeSeparatesInkFromNoisyPaper(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	img := blankForm(200, 200)
	for i := range img.Pix {
		img.Pix[i] = uint8(205 + rng.IntN(51))
	}
	fillRect(img, 100, 100, 4, 4)

	src, err := gocv.ImageGrayToMatGray(img)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ink, err := NewDetector().binarize(src)
	if err != nil {
		t.Fatal(err)
	}
	defer ink.Close()

	if frac := inkFraction(ink); frac > 0.01 {
		t.Errorf("ink fraction = %.3f, want the paper noise excluded", frac)
	}
	if ink.GetUCharAt(102, 102) == 0 {
		t.Error("the drawn mark was not detected as ink")
	}
}

// A solid mark must stay solid: adaptive thresholding on its own hollows out
// the middle of a large dark area, which would turn a filled blob into a ring
// and then into a bogus empty checkbox.
func TestBinarizeKeepsSolidAreasSolid(t *testing.T) {
	img := blankForm(120, 120)
	fillRect(img, 30, 30, 50, 50)

	src, err := gocv.ImageGrayToMatGray(img)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	ink, err := NewDetector().binarize(src)
	if err != nil {
		t.Fatal(err)
	}
	defer ink.Close()

	if ink.GetUCharAt(55, 55) == 0 {
		t.Error("the middle of the filled square is not ink")
	}
}

func benchForm(w, h int) *image.Gray {
	rng := rand.New(rand.NewPCG(7, 8))
	img := blankForm(w, h)
	for i := range img.Pix {
		img.Pix[i] = uint8(205 + rng.IntN(51))
	}
	for y := 100; y+40 < h; y += 140 {
		for x := 100; x+40 < w; x += 340 {
			drawBox(img, x, y, 34, 2)
			if (x+y)%3 == 0 {
				drawCross(img, x, y, 34)
			}
		}
	}
	return img
}

func BenchmarkDetect(b *testing.B) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"2400x1600", 2400, 1600},
		{"maxPixels", 4000, 6000},
	}
	for _, s := range sizes {
		img := benchForm(s.w, s.h)
		b.Run(s.name, func(b *testing.B) {
			d := NewDetector()
			for b.Loop() {
				mustDetect(b, d, img)
			}
		})
	}
}

// The committed form image is the case that exposed the threshold being too
// tight: its cross marks are thin, and measuring everything inside the border
// put them at 0.135 ink against a 0.15 cutoff, so both read as unchecked.
// A ruled table cell is a rectangle with a complete border and a clean
// interior: by shape there is nothing to tell it from a checkbox. What gives
// it away is that it is the width of its column rather than the width of a
// checkbox, and a printed form draws every checkbox the same size. image2 has
// a narrow column beside each checkbox and reported all 13 of its cells.
//
// All 13 of this page's cells are narrower than its boxes, which is the side
// of the median the prior is strict about, so the page comes out exactly
// right. image4's cells are on the other side and four of them survive.
func TestDetectDropsCellsOfTheWrongSize(t *testing.T) {
	got := mustDetect(t, NewDetector(), loadPNG(t, "testdata/image2.png"))

	if len(got) != 48 {
		t.Errorf("found %d checkboxes, want 48", len(got))
	}
	checked := 0
	for _, d := range got {
		if d.Checked {
			checked++
		}
		if d.Box.Width < 20 {
			t.Errorf("reported a %dx%d box at (%d,%d); the ruled cells are 17-19px",
				d.Box.Width, d.Box.Height, d.Box.X, d.Box.Y)
		}
	}
	if checked != 12 {
		t.Errorf("found %d marked checkboxes, want 12", checked)
	}
}

// The prior needs enough boxes to be evidence, it judges each dimension
// against the page's own median, and it is stricter about a box being too
// small than too large.
func TestDropOffSizeBoxes(t *testing.T) {
	const under, over = 0.15, 0.25
	page := func(n int, w, h int) []detection {
		out := make([]detection, n)
		for i := range out {
			out[i] = detection{Box: box{X: i * 50, Width: w, Height: h}}
		}
		return out
	}
	with := func(n int, b box) []detection {
		return append(page(n, 20, 20), detection{Box: b})
	}

	for _, c := range []struct {
		name string
		dets []detection
		want int
	}{
		{"far too wide", with(12, box{Width: 40, Height: 20}), 12},
		{"a shade too narrow", with(12, box{Width: 16, Height: 20}), 12},
		{"too short", with(12, box{Width: 20, Height: 16}), 12},
		{"oversized within tolerance", with(12, box{Width: 24, Height: 24}), 13},
		{"undersized within tolerance", with(12, box{Width: 18, Height: 18}), 13},
		// Too few boxes to establish a median: nothing is dropped.
		{"sparse page", with(4, box{Width: 40, Height: 20}), 5},
	} {
		if got := dropOffSizeBoxes(c.dets, under, over); len(got) != c.want {
			t.Errorf("%s: kept %d of %d boxes, want %d", c.name, len(got), len(c.dets), c.want)
		}
	}
}

// An appraisal form labels its sections with a word set vertically down the
// margin, white letters knocked out of a solid black band. Their counters are
// neat little rectangles on a field of ink, and the detector read ten of them
// on image4 and two on image3 as checkboxes - all of them "checked", since
// the band around them is ink.
//
// This does not fix every false positive on the page: nine ruled table cells
// holding a word, 32-39px wide against the page's 25-27px boxes, still get
// through. Squareness cannot separate those - this scan's real boxes measure
// 0.77-0.85 and the cells 0.61-0.75, and the two ranges touch.
func TestDetectIgnoresLetteringInSolidBanners(t *testing.T) {
	got := mustDetect(t, NewDetector(), loadPNG(t, "testdata/image4.png"))

	// The band runs down the left edge and is about 36px wide. Nothing on
	// this form sits wholly inside it - the leftmost real checkbox starts at
	// x=39 and runs to x=68.
	for _, d := range got {
		if d.Box.X+d.Box.Width <= 36 {
			t.Errorf("reported a checkbox at (%d,%d) %dx%d, inside the section banner",
				d.Box.X, d.Box.Y, d.Box.Width, d.Box.Height)
		}
	}
	// And the guard must not have worked by rejecting the page.
	if len(got) < 110 {
		t.Errorf("found %d checkboxes, want at least 110", len(got))
	}
}

// The margin has to follow the page: large enough to ignore grain on a noisy
// scan, small enough to see a grey border on a clean one. Neither value works
// for the other page, which is why it is measured rather than fixed.
func TestThresholdCFollowsImageNoise(t *testing.T) {
	d := NewDetector()

	quiet := blur(blankForm(200, 200))
	rng := rand.New(rand.NewPCG(11, 12))
	grainy := blankForm(200, 200)
	for i := range grainy.Pix {
		grainy.Pix[i] = uint8(200 + rng.IntN(56))
	}

	for _, c := range []struct {
		name string
		img  *image.Gray
		want float32
	}{
		{"quiet", quiet, minThresholdC},
		{"grainy", grainy, d.ThresholdC},
	} {
		src, err := gocv.ImageGrayToMatGray(c.img)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.thresholdC(src)
		src.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s page: margin = %v, want %v", c.name, got, c.want)
		}
	}
}

// image1 is a form scanned at about 150 DPI, where a checkbox is 16-17px.
// At that size the polygon tolerance fell below the precision of the image
// itself: 0.02 of a 64px perimeter is 1.3px, and a rasterized edge wanders
// about a pixel either way, so every anti-aliased corner became an extra
// vertex. Perfect squares came back as pentagons and were thrown out for not
// being quadrilaterals - 20 of this page's boxes, a third of them.
//
// The page is also the clearest example of the table-rule limitation: its
// marked boxes are fused to the form's grid, so their enclosing contour is
// the whole 1081x1648 table and the X breaks up the interior hole that would
// otherwise find them. That is why so few of these read as checked, and it
// is why this test does not assert a count of them.
func TestDetectFindsSmallBoxesRejectedAsPentagons(t *testing.T) {
	got := mustDetect(t, NewDetector(), loadPNG(t, "testdata/image1.png"))
	if len(got) < 55 {
		t.Errorf("found %d checkboxes, want at least 55", len(got))
	}

	// Two of the boxes that were lost. Both measured squareness 1.00,
	// rectangularity 0.87 and edge coverage 1.00 - every gate that describes
	// a checkbox said yes, and only the corner count said no.
	for _, want := range []box{
		{X: 814, Y: 169, Width: 16, Height: 16},
		{X: 711, Y: 169, Width: 17, Height: 16},
	} {
		found := false
		for _, d := range got {
			if iou(d.Box, want) > 0.5 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no checkbox at %+v", want)
		}
	}
}

func TestDetectMarksThinCrossesAsChecked(t *testing.T) {
	got := mustDetect(t, NewDetector(), loadPNG(t, "testdata/form.png"))
	if len(got) != 4 {
		t.Fatalf("found %d checkboxes, want 4: %+v", len(got), got)
	}

	want := []bool{false, true, true, false}
	for i, d := range got {
		if d.Checked != want[i] {
			t.Errorf("[%d] at x=%d checked = %v, want %v", i, d.Box.X, d.Checked, want[i])
		}
	}
}
