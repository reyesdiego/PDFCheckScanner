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

// drawBox draws a hollow square with the given stroke thickness.
func drawBox(img *image.Gray, x, y, side, stroke int) {
	for t := range stroke {
		for i := range side {
			ink(img, x+i, y+t)
			ink(img, x+i, y+side-1-t)
			ink(img, x+t, y+i)
			ink(img, x+side-1-t, y+i)
		}
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
func TestDetectMarksThinCrossesAsChecked(t *testing.T) {
	f, err := os.Open("testdata/form.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	got := mustDetect(t, NewDetector(), img)
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
