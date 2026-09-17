package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"
	"sort"
)

// Detector finds checkboxes in a form and reports whether each one is marked.
//
// It works geometrically rather than with a trained model: the image is
// binarized, split into connected runs of ink, and each run is scored on how
// much it looks like a hollow square. A candidate is "checked" when enough ink
// sits inside its border. The assumption is dark ink on a lighter background,
// which is what scans and photos of paper forms give you.
type Detector struct {
	// MinSide is the smallest checkbox side, in pixels, worth reporting.
	MinSide int
	// MaxSideFrac caps a checkbox side as a fraction of the shorter image edge.
	MaxSideFrac float64
	// MinSquareness is the lowest min(w,h)/max(w,h) a candidate may have.
	MinSquareness float64
	// MinBorderScore is the fraction of the bounding-box ring that must be inked.
	MinBorderScore float64
	// MaxInteriorInk rejects candidates whose interior is essentially solid,
	// which is a blob or a glyph rather than a box with a mark in it.
	MaxInteriorInk float64
	// CheckedInk is the interior ink fraction at which a box counts as checked.
	CheckedInk float64
	// MaxInkFrac skips images that are mostly ink, where "dark on light" does
	// not hold and the binarization would be meaningless.
	MaxInkFrac float64
	// MaxDetections caps how many checkboxes a single image can report.
	MaxDetections int
}

// NewDetector returns a Detector tuned for checkboxes on scanned forms.
func NewDetector() *Detector {
	return &Detector{
		MinSide:        8,
		MaxSideFrac:    0.5,
		MinSquareness:  0.65,
		MinBorderScore: 0.75,
		MaxInteriorInk: 0.85,
		CheckedInk:     0.07,
		MaxInkFrac:     0.6,
		MaxDetections:  500,
	}
}

// Detect returns the checkboxes found in img, in reading order: top to bottom,
// then left to right. Box coordinates are pixels from the image's top-left.
func (d *Detector) Detect(img image.Image) []detection {
	bm := binarize(img)
	if bm == nil || bm.inkFrac() > d.MaxInkFrac {
		return []detection{}
	}

	labels, comps := bm.components()
	found := make([]detection, 0, len(comps))
	for _, c := range comps {
		if det, ok := d.classify(bm, labels, c); ok {
			found = append(found, det)
		}
	}

	found = dedupe(found)
	if len(found) > d.MaxDetections {
		found = found[:d.MaxDetections]
	}
	sortReadingOrder(found)
	return found
}

// classify scores one connected component as a checkbox candidate.
func (d *Detector) classify(bm *bitmap, labels []int32, c component) (detection, bool) {
	w, h := c.maxX-c.minX+1, c.maxY-c.minY+1
	side := max(w, h)

	if side < d.MinSide || side > int(d.MaxSideFrac*float64(min(bm.w, bm.h))) {
		return detection{}, false
	}
	squareness := float64(min(w, h)) / float64(side)
	if squareness < d.MinSquareness {
		return detection{}, false
	}
	border := borderScore(bm, labels, c)
	if border < d.MinBorderScore {
		return detection{}, false
	}
	fill, ok := interiorInk(bm, c)
	if !ok || fill > d.MaxInteriorInk {
		return detection{}, false
	}

	// How far the interior ink sits from the checked/unchecked boundary, which
	// is what makes a faint smudge or a barely-marked box less certain.
	margin := math.Min(math.Abs(fill-d.CheckedInk)/d.CheckedInk, 1)
	conf := 0.5*border + 0.3*squareness + 0.2*margin

	return detection{
		Label:      "checkbox",
		Confidence: math.Round(conf*100) / 100,
		Box:        box{X: c.minX, Y: c.minY, Width: w, Height: h},
		Checked:    fill >= d.CheckedInk,
	}, true
}

// borderScore is the fraction of the bounding box's four edges that the
// component actually covers. A complete rectangle scores 1; an arc, a letter
// or a stray stroke scores much lower.
func borderScore(bm *bitmap, labels []int32, c component) float64 {
	w, h := c.maxX-c.minX+1, c.maxY-c.minY+1
	band := max(1, int(math.Round(0.1*float64(max(w, h)))))

	covered, total := 0, 0
	hit := func(x0, y0, x1, y1 int) bool {
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if labels[y*bm.w+x] == c.label {
					return true
				}
			}
		}
		return false
	}

	for x := c.minX; x <= c.maxX; x++ {
		total += 2
		if hit(x, c.minY, x, min(c.minY+band, c.maxY)) {
			covered++
		}
		if hit(x, max(c.maxY-band, c.minY), x, c.maxY) {
			covered++
		}
	}
	for y := c.minY; y <= c.maxY; y++ {
		total += 2
		if hit(c.minX, y, min(c.minX+band, c.maxX), y) {
			covered++
		}
		if hit(max(c.maxX-band, c.minX), y, c.maxX, y) {
			covered++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total)
}

// interiorInk is the fraction of inked pixels strictly inside a candidate's
// border. It counts all ink, not just this component's, so a tick mark that
// never touches the box still registers.
func interiorInk(bm *bitmap, c component) (float64, bool) {
	side := max(c.maxX-c.minX+1, c.maxY-c.minY+1)
	inset := max(2, int(math.Round(0.15*float64(side))))

	x0, x1 := c.minX+inset, c.maxX-inset
	y0, y1 := c.minY+inset, c.maxY-inset
	if x1 < x0 || y1 < y0 {
		return 0, false
	}

	ink := 0
	for y := y0; y <= y1; y++ {
		row := y * bm.w
		for x := x0; x <= x1; x++ {
			if bm.ink[row+x] {
				ink++
			}
		}
	}
	return float64(ink) / float64((x1-x0+1)*(y1-y0+1)), true
}

// dedupe drops candidates that overlap or nest inside a more confident one,
// which is what a double-ruled border or a box-in-a-box produces.
func dedupe(dets []detection) []detection {
	sort.SliceStable(dets, func(i, j int) bool {
		return dets[i].Confidence > dets[j].Confidence
	})

	kept := make([]detection, 0, len(dets))
	for _, det := range dets {
		clash := false
		for _, k := range kept {
			if iou(det.Box, k.Box) > 0.3 || nests(det.Box, k.Box) {
				clash = true
				break
			}
		}
		if !clash {
			kept = append(kept, det)
		}
	}
	return kept
}

func iou(a, b box) float64 {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.Width, b.X+b.Width), min(a.Y+a.Height, b.Y+b.Height)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	inter := (x1 - x0) * (y1 - y0)
	union := a.Width*a.Height + b.Width*b.Height - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// nests reports whether either box sits entirely within the other.
func nests(a, b box) bool {
	inside := func(inner, outer box) bool {
		return inner.X >= outer.X && inner.Y >= outer.Y &&
			inner.X+inner.Width <= outer.X+outer.Width &&
			inner.Y+inner.Height <= outer.Y+outer.Height
	}
	return inside(a, b) || inside(b, a)
}

// sortReadingOrder sorts top to bottom, then left to right, grouping boxes
// into rows so that a few pixels of vertical skew does not scramble a row.
func sortReadingOrder(dets []detection) {
	if len(dets) == 0 {
		return
	}
	heights := make([]int, len(dets))
	for i, d := range dets {
		heights[i] = d.Box.Height
	}
	slices.Sort(heights)
	rowHeight := max(1, heights[len(heights)/2])

	row := func(d detection) int { return (d.Box.Y + d.Box.Height/2) / rowHeight }
	sort.SliceStable(dets, func(i, j int) bool {
		if ri, rj := row(dets[i]), row(dets[j]); ri != rj {
			return ri < rj
		}
		return dets[i].Box.X < dets[j].Box.X
	})
}

// bitmap is a binarized image: ink is true where a pixel is darker than the
// threshold picked for the whole image.
type bitmap struct {
	w, h int
	ink  []bool
}

// binarize converts img to a bitmap using Sauvola's local thresholding, which
// compares each pixel to the mean and spread of its neighbourhood.
//
// A single global threshold (Otsu) is not good enough here: on a page where
// ink covers well under 1% of the pixels, the threshold that best splits the
// histogram is one that cuts the noisy paper distribution in half, which turns
// half the page into "ink" and buries the checkboxes. Thresholding locally also
// absorbs uneven lighting in photographed forms.
func binarize(img image.Image) *bitmap {
	b := img.Bounds()
	if b.Dx() < 3 || b.Dy() < 3 {
		return nil
	}
	w, h := b.Dx(), b.Dy()

	gray := image.NewGray(image.Rect(0, 0, w, h))
	// Fill white first and compose over it: a transparent PNG background would
	// otherwise land on black and read as solid ink.
	draw.Draw(gray, gray.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(gray, gray.Bounds(), img, b.Min, draw.Over)

	sum, sumSq := integrals(gray.Pix, w, h)
	radius := windowRadius(w, h)

	bm := &bitmap{w: w, h: h, ink: make([]bool, w*h)}
	for y := range h {
		y0, y1 := max(y-radius, 0), min(y+radius, h-1)
		for x := range w {
			x0, x1 := max(x-radius, 0), min(x+radius, w-1)
			n := float64((x1 - x0 + 1) * (y1 - y0 + 1))

			total := float64(rectSum(sum, w, x0, y0, x1, y1))
			totalSq := float64(rectSum(sumSq, w, x0, y0, x1, y1))
			mean := total / n
			variance := totalSq/n - mean*mean
			if variance < 0 {
				variance = 0
			}

			// Sauvola: on flat paper the threshold sits at (1-k) of the local
			// mean; where contrast is high it moves towards the mean.
			t := mean * (1 + sauvolaK*(math.Sqrt(variance)/sauvolaRange-1))
			bm.ink[y*w+x] = float64(gray.Pix[y*w+x]) <= t
		}
	}
	return bm
}

const (
	sauvolaK     = 0.2
	sauvolaRange = 128.0
)

// windowRadius scales the neighbourhood with the image so that a window covers
// a checkbox and some surrounding paper, but never the whole page.
func windowRadius(w, h int) int {
	r := min(w, h) / 40
	return min(max(r, 7), 50)
}

// integrals builds summed-area tables of the pixel values and their squares,
// which make the local mean and variance constant-time per pixel.
func integrals(pix []uint8, w, h int) (sum, sumSq []int64) {
	sum = make([]int64, (w+1)*(h+1))
	sumSq = make([]int64, (w+1)*(h+1))
	for y := range h {
		var rowSum, rowSumSq int64
		for x := range w {
			v := int64(pix[y*w+x])
			rowSum += v
			rowSumSq += v * v
			i := (y+1)*(w+1) + x + 1
			sum[i] = sum[i-(w+1)] + rowSum
			sumSq[i] = sumSq[i-(w+1)] + rowSumSq
		}
	}
	return sum, sumSq
}

// rectSum returns the total over the inclusive rectangle from a summed-area
// table built by integrals.
func rectSum(table []int64, w, x0, y0, x1, y1 int) int64 {
	stride := w + 1
	a := table[y0*stride+x0]
	b := table[y0*stride+x1+1]
	c := table[(y1+1)*stride+x0]
	d := table[(y1+1)*stride+x1+1]
	return d - b - c + a
}

func (bm *bitmap) inkFrac() float64 {
	n := 0
	for _, v := range bm.ink {
		if v {
			n++
		}
	}
	return float64(n) / float64(len(bm.ink))
}

// component is one connected run of ink, described by its bounding box.
type component struct {
	label                  int32
	minX, minY, maxX, maxY int
	pixels                 int
}

// components labels every connected run of ink using 8-connectivity, so a
// border with single-pixel diagonal gaps still comes back as one shape.
func (bm *bitmap) components() ([]int32, []component) {
	labels := make([]int32, len(bm.ink))
	var comps []component
	stack := make([]int, 0, 1024)

	var next int32
	for start := range bm.ink {
		if !bm.ink[start] || labels[start] != 0 {
			continue
		}
		next++
		c := component{
			label: next,
			minX:  start % bm.w, maxX: start % bm.w,
			minY: start / bm.w, maxY: start / bm.w,
		}

		labels[start] = next
		stack = append(stack, start)
		for len(stack) > 0 {
			idx := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := idx%bm.w, idx/bm.w

			c.pixels++
			c.minX, c.maxX = min(c.minX, x), max(c.maxX, x)
			c.minY, c.maxY = min(c.minY, y), max(c.maxY, y)

			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= bm.w || ny >= bm.h {
						continue
					}
					n := ny*bm.w + nx
					if bm.ink[n] && labels[n] == 0 {
						labels[n] = next
						stack = append(stack, n)
					}
				}
			}
		}
		comps = append(comps, c)
	}
	return labels, comps
}
