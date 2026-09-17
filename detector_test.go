package main

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
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

	got := NewDetector().Detect(img)
	if len(got) != 3 {
		t.Fatalf("found %d checkboxes, want 3: %+v", len(got), got)
	}

	wantChecked := []bool{false, true, true}
	wantX := []int{20, 90, 160}
	for i, d := range got {
		if d.Label != "checkbox" {
			t.Errorf("[%d] label = %q, want checkbox", i, d.Label)
		}
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

	got := NewDetector().Detect(img)
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

	got := NewDetector().Detect(img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
	if got[0].Checked {
		t.Error("checked = true, want false")
	}
	wantBox(t, got[0].Box, 40, 40, 90, 90)
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
			if got := NewDetector().Detect(img); len(got) != 0 {
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

	got := NewDetector().Detect(img)
	if len(got) != 1 {
		t.Fatalf("found %d checkboxes, want 1: %+v", len(got), got)
	}
}

func TestDetectSortsInReadingOrder(t *testing.T) {
	img := blankForm(200, 200)
	// Drawn out of order, and with a few pixels of row skew.
	drawBox(img, 120, 122, 24, 1)
	drawBox(img, 30, 30, 24, 1)
	drawBox(img, 120, 28, 24, 1)
	drawBox(img, 28, 120, 24, 1)

	got := NewDetector().Detect(img)
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

	got := NewDetector().Detect(img)
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

	got := NewDetector().Detect(img)
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
	if got := NewDetector().Detect(img); len(got) != 0 {
		t.Errorf("found %d checkboxes, want 0: %+v", len(got), got)
	}
}

func TestDetectReturnsEmptySliceNotNil(t *testing.T) {
	if got := NewDetector().Detect(blankForm(40, 40)); got == nil {
		t.Error("Detect returned nil, want an empty slice")
	}
}

func TestDetectHandlesTinyImage(t *testing.T) {
	if got := NewDetector().Detect(blankForm(2, 2)); len(got) != 0 {
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

	got := NewDetector().Detect(img)
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

	got := NewDetector().Detect(img)
	if len(got) != 3 {
		t.Fatalf("found %d checkboxes, want 3: %+v", len(got), got)
	}
	if got[0].Checked || !got[1].Checked || got[2].Checked {
		t.Errorf("checked = [%v %v %v], want [false true false]",
			got[0].Checked, got[1].Checked, got[2].Checked)
	}
}

func TestSauvolaSeparatesInkFromNoisyPaper(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	img := blankForm(200, 200)
	for i := range img.Pix {
		img.Pix[i] = uint8(205 + rng.IntN(51))
	}
	fillRect(img, 100, 100, 4, 4)

	bm := binarize(img)
	if bm == nil {
		t.Fatal("binarize returned nil")
	}
	if frac := bm.inkFrac(); frac > 0.01 {
		t.Errorf("ink fraction = %.3f, want the paper noise excluded", frac)
	}
	if !bm.ink[102*200+102] {
		t.Error("the drawn mark was not detected as ink")
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
				d.Detect(img)
			}
		})
	}
}
