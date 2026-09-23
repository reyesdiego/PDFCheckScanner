//go:build docs

// Regenerates the figures in docs/img that docs/PIPELINE.md walks through:
//
//	make docs
//
// They are built from the committed fixtures by the real detector, so they
// cannot drift from what the code does. The before-and-after pairs are made
// by turning one gate off through its Detector field, which is why every
// threshold in the detector is a field rather than a constant.

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gocv.io/x/gocv"
)

const docsDir = "docs/img"

// walkthrough is the crop every pipeline figure is drawn from: three rows of
// a neighbourhood section, twelve checkboxes, half of them marked, with the
// table rules and body text that make the page hard.
var walkthrough = struct {
	file string
	r    image.Rectangle
}{"testdata/image4.png", image.Rect(40, 540, 700, 625)}

var (
	red   = color.RGBA{R: 220, A: 255}
	green = color.RGBA{G: 150, A: 255}
	blue  = color.RGBA{B: 220, A: 255}
	amber = color.RGBA{R: 230, G: 140, A: 255}
	grey  = color.RGBA{R: 130, G: 130, B: 130, A: 255}
)

func TestGenerateDocImages(t *testing.T) {
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := loadPNG(t, walkthrough.file)
	crop := cropOf(src, walkthrough.r)

	writeFigure(t, "01-source", crop, 2)
	writeFigure(t, "02-ink", inkMaskImage(t, src, walkthrough.r), 2)
	writeFigure(t, "03-contours", contourImage(t, src, walkthrough.r), 2)
	writeFigure(t, "04-gates", gatesImage(t, src), 6)
	writeFigure(t, "05-result", overlay(t, NewDetector(), src, walkthrough.r), 2)

	// Each pair is a bug the samples found, shown with the gate that fixes
	// it switched off and then on.
	beforeAfter(t, "10-pentagon", "testdata/image1.png", image.Rect(700, 160, 1100, 200), 2,
		func(d *Detector) { d.MinApproxEpsilon = 0 })
	// Two independent guards catch the banner lettering - it is both on ink
	// rather than paper, and far off the page's box size - so both have to
	// come off to show what it looked like.
	beforeAfter(t, "11-banner", "testdata/image4.png", image.Rect(0, 170, 260, 420), 2,
		func(d *Detector) { d.MaxSurroundingInk, d.UndersizeFrac, d.OversizeFrac = 1.1, 0, 0 })
	beforeAfter(t, "12-cells", "testdata/image2.png", image.Rect(780, 300, 1100, 400), 3,
		func(d *Detector) { d.UndersizeFrac = 0 })
}

// cropOf returns a copy of r out of img.
func cropOf(img image.Image, r image.Rectangle) *image.RGBA {
	r = r.Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

// writeFigure scales an image by a whole number and writes it to docs/img.
func writeFigure(t *testing.T, name string, img image.Image, scale int) {
	t.Helper()
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*scale, b.Dy()*scale))
	for y := range b.Dy() * scale {
		for x := range b.Dx() * scale {
			out.Set(x, y, img.At(b.Min.X+x/scale, b.Min.Y+y/scale))
		}
	}

	path := filepath.Join(docsDir, name+".png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%dx%d)", path, out.Bounds().Dx(), out.Bounds().Dy())
}

// masksOf binarizes a whole page exactly as Detect does.
func masksOf(t *testing.T, img image.Image) (gocv.Mat, gocv.Mat, func()) {
	t.Helper()
	d := NewDetector()
	src, err := gocv.ImageGrayToMatGray(toGray(img))
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.thresholdC(src)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := d.binarizeAt(src, c)
	if err != nil {
		t.Fatal(err)
	}
	return src, shape, func() { src.Close(); shape.Close() }
}

// inkMaskImage renders the binarized page: ink white, paper black, as the
// detector sees it.
func inkMaskImage(t *testing.T, img image.Image, r image.Rectangle) image.Image {
	t.Helper()
	_, shape, done := masksOf(t, img)
	defer done()

	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := range r.Dy() {
		for x := range r.Dx() {
			v := shape.GetUCharAt(r.Min.Y+y, r.Min.X+x)
			out.Set(x, y, color.Gray{Y: v})
		}
	}
	return out
}

// contourImage draws every contour the detector has to sift through.
func contourImage(t *testing.T, img image.Image, r image.Rectangle) image.Image {
	t.Helper()
	_, shape, done := masksOf(t, img)
	defer done()

	hierarchy := gocv.NewMat()
	defer hierarchy.Close()
	contours := gocv.FindContoursWithParams(shape, &hierarchy, gocv.RetrievalList, gocv.ChainApproxSimple)
	defer contours.Close()

	// Drawn with OpenCV rather than by plotting the stored points: the
	// contours are chain-approximated, so their points are just the corners.
	canvas := gocv.NewMatWithSizeFromScalar(gocv.NewScalar(255, 255, 255, 0),
		shape.Rows(), shape.Cols(), gocv.MatTypeCV8UC3)
	defer canvas.Close()

	palette := []color.RGBA{red, blue, amber, green, grey}
	n := 0
	for i := range contours.Size() {
		if !gocv.BoundingRect(contours.At(i)).Overlaps(r) {
			continue
		}
		gocv.DrawContours(&canvas, contours, i, palette[n%len(palette)], 1)
		n++
	}
	t.Logf("03-contours: %d contours in the crop", n)

	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := range r.Dy() {
		for x := range r.Dx() {
			v := canvas.GetVecbAt(r.Min.Y+y, r.Min.X+x)
			out.Set(x, y, color.RGBA{R: v[2], G: v[1], B: v[0], A: 255})
		}
	}
	return out
}

// gatesImage annotates one checkbox with what every gate measures.
func gatesImage(t *testing.T, img image.Image) image.Image {
	t.Helper()
	_, shape, done := masksOf(t, img)
	defer done()

	r := firstMarkedBox(t, img, walkthrough.r)
	view := r.Inset(-10)
	out := cropOf(img, view)
	off := func(p image.Point) image.Point { return p.Sub(view.Min) }

	// The bounding box itself, drawn just outside so the bands do not cover
	// it.
	strokeRect(out, image.Rectangle{off(r.Min), off(r.Max)}.Inset(-2), red)

	// The bands edgeCoverage reduces, one per side.
	band := max(1, int(0.12*float64(max(r.Dx(), r.Dy()))))
	for _, b := range []image.Rectangle{
		{r.Min, image.Pt(r.Max.X, r.Min.Y+band)},
		{image.Pt(r.Min.X, r.Max.Y-band), r.Max},
		{r.Min, image.Pt(r.Min.X+band, r.Max.Y)},
		{image.Pt(r.Max.X-band, r.Min.Y), r.Max},
	} {
		strokeRect(out, image.Rectangle{off(b.Min), off(b.Max)}, amber)
	}

	// The two interior windows, inset past the measured border.
	stroke := strokeWidth(shape, r)
	for frac, c := range map[float64]color.RGBA{interiorWideFrac: blue, interiorFrac: green} {
		dx, dy := inset(frac, r.Dx(), stroke), inset(frac, r.Dy(), stroke)
		in := image.Rect(r.Min.X+dx, r.Min.Y+dy, r.Max.X-dx, r.Max.Y-dy)
		strokeRect(out, image.Rectangle{off(in.Min), off(in.Max)}, c)
	}

	cover, _ := edgeCoverage(shape, r)
	central, wide, _ := interiorInk(shape, shape, r)
	t.Logf("04-gates: stroke=%dpx cover=%.2f central=%.2f wide=%.2f", stroke, cover, central, wide)
	return out
}

// firstMarkedBox is a real detection to annotate, so the figure shows the
// windows where the detector actually put them.
func firstMarkedBox(t *testing.T, img image.Image, within image.Rectangle) image.Rectangle {
	t.Helper()
	got, err := NewDetector().Detect(img)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		r := image.Rect(d.Box.X, d.Box.Y, d.Box.X+d.Box.Width, d.Box.Y+d.Box.Height)
		if d.Checked && r.In(within) {
			return r
		}
	}
	t.Fatalf("no marked checkbox inside %v", within)
	return image.Rectangle{}
}

func strokeRect(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for x := r.Min.X; x < r.Max.X; x++ {
		img.Set(x, r.Min.Y, c)
		img.Set(x, r.Max.Y-1, c)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		img.Set(r.Min.X, y, c)
		img.Set(r.Max.X-1, y, c)
	}
}

// overlay draws a detector's answers over a crop: green marked, red not.
func overlay(t *testing.T, d *Detector, img image.Image, r image.Rectangle) image.Image {
	t.Helper()
	got, err := d.Detect(img)
	if err != nil {
		t.Fatal(err)
	}
	out := cropOf(img, r)
	in := 0
	for _, x := range got {
		box := image.Rect(x.Box.X, x.Box.Y, x.Box.X+x.Box.Width, x.Box.Y+x.Box.Height)
		if !box.Overlaps(r) {
			continue
		}
		in++
		c := red
		if x.Checked {
			c = green
		}
		box = box.Inset(-2).Sub(r.Min)
		strokeRect(out, box, c)
		strokeRect(out, box.Inset(-1), c)
	}
	t.Logf("%d detections in the crop", in)
	return out
}

// beforeAfter writes one figure with a gate disabled and one with it on.
func beforeAfter(t *testing.T, name, file string, r image.Rectangle, scale int, disable func(*Detector)) {
	t.Helper()
	img := loadPNG(t, file)

	off := NewDetector()
	disable(off)
	t.Logf("%s: before", name)
	writeFigure(t, name+"-before", overlay(t, off, img, r), scale)
	t.Logf("%s: after", name)
	writeFigure(t, name+"-after", overlay(t, NewDetector(), img, r), scale)

	all, _ := NewDetector().Detect(img)
	was, _ := off.Detect(img)
	fmt.Printf("%s whole page: %d -> %d detections\n", name, len(was), len(all))
}
