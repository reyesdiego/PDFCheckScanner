package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sort"

	"gocv.io/x/gocv"
)

// Detector finds checkboxes in a document image and reports whether each one
// is marked.
//
// It looks for quadrilaterals rather than for square-ish blobs of ink. Every
// contour in the binarized page is fitted with a polygon, and a candidate has
// to come back as a four-sided shape that fills its own bounding box. That is
// what separates a ruled box from a letter: "D" and "o" are as square as a
// checkbox and have hollow middles, but neither fits a quadrilateral.
//
// Contours also give the interiors for free. An unchecked box encloses a hole,
// and a hole is a contour in its own right, so a box still registers when its
// border runs into a table rule and its outer contour is lost to the grid.
type Detector struct {
	// MinSide is the smallest checkbox side, in pixels, worth reporting. Below
	// roughly 12px anti-aliasing fills a box's interior and there is nothing
	// left to measure, so scans want to be 200-300 DPI. PDFs are rasterized at
	// rasterDPI for this reason.
	MinSide int
	// MaxSide is the floor on the largest checkbox side, in pixels. A checkbox
	// is a physical thing, under about a centimetre across, which is ~120px at
	// the 300 DPI that PDFs are rasterized at, and an absolute bound keeps
	// table cells and page frames out better than a relative one. But an
	// upload to the image endpoint carries no DPI, so the bound cannot be
	// absolute alone: a 16pt box scanned at 600 DPI is ~133px and would be
	// thrown out, leaving a perfectly good scan with no detections at all. The
	// effective cap is therefore the larger of this and MaxSideShortEdgeFrac.
	MaxSide int
	// MaxSideShortEdgeFrac scales the cap with the image, so a page scanned at
	// twice the DPI gets twice the allowance. A centimetre is about 4% of the
	// short edge of a portrait letter page, whatever resolution it was scanned
	// at; the value is set loosely above that. It only ever raises the cap
	// above MaxSide, so a crop the size of one checkbox is unaffected.
	MaxSideShortEdgeFrac float64
	// MaxSideFrac additionally stops a box from being most of the image, for
	// crops barely bigger than one checkbox. It applies to the shorter edge,
	// so it must stay generous: a single form row cropped out of a page is
	// wide and short, and at 0.5 the height of a 58px-tall strip capped
	// detection at 29px and rejected every 33px checkbox in it.
	MaxSideFrac float64
	// MinSquareness is the lowest min(w,h)/max(w,h) a candidate may have.
	MinSquareness float64
	// MinRectangularity is how much of its bounding box the candidate's convex
	// hull must enclose. A rectangle scores 1; a triangle or a curve far less.
	MinRectangularity float64
	// MinEdgeCoverage is the fraction of each side of the bounding box that
	// must carry ink near it. A hull is blind to what is missing from the
	// middle of a side, and blocky capitals like H, E and N have rectangular
	// hulls, so this is what keeps them out.
	MinEdgeCoverage float64
	// ApproxEpsilonFrac is the polygon-fitting tolerance, as a fraction of the
	// contour's perimeter. It has to be tight: at 0.05 the counter of a bold
	// "B" or "D" fits a quadrilateral and heading text reads as checkboxes,
	// while at 0.02 the prose pages of a document come back empty and the forms
	// keep all but a handful of their boxes.
	ApproxEpsilonFrac float64
	// MaxInteriorInk rejects candidates whose interior is essentially solid,
	// which is a blob or a glyph rather than a box with a mark in it.
	MaxInteriorInk float64
	// CheckedInk is the ink fraction at which the central half of a box counts
	// as marked.
	CheckedInk float64
	// CheckedWideInk is the same test over a wider window, inset 15%, for
	// marks whose ink sits in the box's corners rather than its middle. A worn
	// or scratchy X leaves blobs at the four corners and almost nothing in the
	// centre, and the central half then reads it as empty - and, worse, reads
	// it differently depending on how large the box is rendered.
	CheckedWideInk float64
	// MinConfidence rejects candidates that are mediocre on every measure at
	// once. A hand-drawn square passes each individual gate by a hair - hull
	// 0.81 against 0.80, coverage 0.76 against 0.75 - where a printed box
	// clears them all comfortably, so the blended score separates the two
	// where no single threshold can.
	MinConfidence float64
	// ThresholdC is how much darker than its neighbourhood a pixel must be to
	// count as ink. Paper grain needs a generous margin: at a small value a
	// third of a noisy scan's background crosses its local mean and the page
	// binarizes to mush. Blurring the input first would fix that too, but it
	// widens every stroke and inflates the reported boxes by a pixel on each
	// side, so the margin does the work instead.
	ThresholdC float32
	// MaxInkFrac skips images that are mostly ink, where "dark on light" does
	// not hold and the binarization would be meaningless.
	MaxInkFrac float64
	// DarkFloor is the grey level below which a pixel is ink no matter what its
	// neighbourhood looks like. Adaptive thresholding alone hollows out large
	// solid areas, because the middle of a blob has no local contrast.
	DarkFloor float32
	// MaxDetections caps how many checkboxes a single image can report.
	MaxDetections int
}

// NewDetector returns a Detector tuned for checkboxes on scanned forms.
func NewDetector() *Detector {
	return &Detector{
		MinSide:              12,
		MaxSide:              120,
		MaxSideShortEdgeFrac: 0.06,
		MaxSideFrac:          0.9,
		MinSquareness:        0.6,
		MinRectangularity:    0.8,
		MinEdgeCoverage:      0.75,
		ApproxEpsilonFrac:    0.02,
		MaxInteriorInk:       0.85,
		CheckedInk:           0.10,
		CheckedWideInk:       0.13,
		MinConfidence:        0.87,
		ThresholdC:           30,
		MaxInkFrac:           0.6,
		DarkFloor:            90,
		MaxDetections:        500,
	}
}

// Detect returns the checkboxes found in img, in reading order: top to bottom,
// then left to right. Box coordinates are pixels from the image's top-left.
//
// TODO: deskew first. A rotated box still fits a quadrilateral, but its
// bounding box no longer matches it, so rectangularity drops and the box is
// lost. Estimating the page angle from its long lines and rotating would make
// photographed documents work.
//
// TODO: erase long horizontal and vertical runs before finding contours. A
// checkbox whose border runs into a table rule is currently found only by its
// interior hole, which means a marked one, whose hole is broken up by the
// mark, can be missed entirely.
//
// An error means the image could not be examined, which is a different answer
// from an empty result and has to stay distinguishable from it: a caller that
// cannot tell a blank form from a broken OpenCV call will record the wrong
// thing and never know. An image too small to hold a checkbox, or one too
// dark to binarize meaningfully, is not an error - it genuinely has nothing
// to report.
func (d *Detector) Detect(img image.Image) ([]detection, error) {
	gray := toGray(img)
	if gray == nil {
		return []detection{}, nil
	}

	src, err := gocv.ImageGrayToMatGray(gray)
	if err != nil {
		return nil, fmt.Errorf("convert image: %w", err)
	}
	defer src.Close()

	ink, err := d.binarize(src)
	if err != nil {
		return nil, fmt.Errorf("binarize image: %w", err)
	}
	defer ink.Close()

	if inkFraction(ink) > d.MaxInkFrac {
		return []detection{}, nil
	}

	found := d.candidates(ink)
	found = dedupe(found)
	if len(found) > d.MaxDetections {
		// dedupe leaves the list ordered by area, and a big candidate is the
		// least likely to be a real checkbox, so capping it as it stands
		// would throw away exactly the detections worth keeping.
		sort.SliceStable(found, func(i, j int) bool {
			return found[i].Confidence > found[j].Confidence
		})
		found = found[:d.MaxDetections]
	}
	sortReadingOrder(found)
	return found, nil
}

// binarize returns a mask where ink is 255. On error it returns the zero Mat,
// which owns nothing and must not be closed: a Mat is C++ heap memory outside
// Go's reach, so handing back a live one the caller drops leaks it for good.
//
// Thresholding is local, so uneven lighting across a photographed page does
// not need tuning per image, with an absolute floor OR-ed in so that large
// solid marks stay solid instead of hollowing out.
func (d *Detector) binarize(src gocv.Mat) (gocv.Mat, error) {
	adaptive := gocv.NewMat()
	defer adaptive.Close()
	if err := gocv.AdaptiveThreshold(src, &adaptive, 255, gocv.AdaptiveThresholdGaussian,
		gocv.ThresholdBinaryInv, blockSize(src.Cols(), src.Rows()), d.ThresholdC); err != nil {
		return gocv.Mat{}, err
	}

	dark := gocv.NewMat()
	defer dark.Close()
	gocv.Threshold(src, &dark, d.DarkFloor, 255, gocv.ThresholdBinaryInv)

	ink := gocv.NewMat()
	if err := gocv.BitwiseOr(adaptive, dark, &ink); err != nil {
		ink.Close()
		return gocv.Mat{}, err
	}

	return ink, nil
}

// candidates fits a polygon to every contour and keeps the ones shaped like a
// checkbox.
func (d *Detector) candidates(ink gocv.Mat) []detection {
	hierarchy := gocv.NewMat()
	defer hierarchy.Close()

	// RetrievalList returns holes alongside outlines, which is what lets an
	// unchecked box be found by its interior when its border is welded to a
	// table rule.
	contours := gocv.FindContoursWithParams(ink, &hierarchy, gocv.RetrievalList, gocv.ChainApproxSimple)
	defer contours.Close()

	shortEdge := float64(min(ink.Cols(), ink.Rows()))
	maxSide := max(d.MaxSide, int(math.Round(d.MaxSideShortEdgeFrac*shortEdge)))
	maxSide = min(maxSide, int(d.MaxSideFrac*shortEdge))
	found := make([]detection, 0, contours.Size())

	for i := range contours.Size() {
		c := contours.At(i)

		// Fit the polygon to the convex hull, not the raw contour. Real marks
		// overflow their box: a bold X pokes out past the border at the
		// corners, and the outline then detours around each spike and stops
		// being a quadrilateral - 7 corners on one real example that was
		// otherwise a perfect 37x37 square. A hull ignores spikes and nicks.
		hullMat := gocv.NewMat()
		if err := gocv.ConvexHull(c, &hullMat, false, true); err != nil {
			hullMat.Close()
			continue
		}
		hull := gocv.NewPointVectorFromMat(hullMat)
		approx := gocv.ApproxPolyDP(hull, d.ApproxEpsilonFrac*gocv.ArcLength(hull, true), true)
		corners := approx.Size()
		hullArea := gocv.ContourArea(hull)
		approx.Close()
		hull.Close()
		hullMat.Close()
		if corners != 4 {
			continue
		}

		r := gocv.BoundingRect(c)
		w, h := r.Dx(), r.Dy()
		side := max(w, h)
		if side < d.MinSide || side > maxSide {
			continue
		}
		squareness := float64(min(w, h)) / float64(side)
		if squareness < d.MinSquareness {
			continue
		}
		rectangularity := hullArea / float64(w*h)
		if rectangularity < d.MinRectangularity {
			continue
		}
		cover, ok := edgeCoverage(ink, r)
		if !ok || cover < d.MinEdgeCoverage {
			continue
		}

		central, wide, ok := interiorInk(ink, r)
		if !ok || central > d.MaxInteriorInk {
			continue
		}
		checked := central >= d.CheckedInk || wide >= d.CheckedWideInk

		// TODO: this ranks candidates sensibly but is not a calibrated
		// probability. With labelled pages it should be fitted instead of
		// weighted by hand, so callers can threshold on it meaningfully.
		margin := math.Min(math.Abs(central-d.CheckedInk)/d.CheckedInk, 1)
		if checked {
			margin = 1
		}
		conf := 0.35*math.Min(rectangularity, 1) + 0.25*cover +
			0.2*squareness + 0.2*margin
		if conf < d.MinConfidence {
			continue
		}

		found = append(found, detection{
			Confidence: math.Round(conf*100) / 100,
			Box:        box{X: r.Min.X, Y: r.Min.Y, Width: w, Height: h},
			Checked:    checked,
		})
	}
	return found
}

// interiorFrac trims this much off each dimension to leave the central half of
// a candidate; interiorWideFrac trims less, leaving a window that still
// excludes the border stroke but reaches into the corners. Both are floors:
// the actual trim also has to clear the border, which on a small box is more
// than either fraction. maxStrokeFrac bounds how much of a side the border is
// allowed to account for, so that a solid blob does not swallow its own
// window.
const (
	interiorFrac     = 0.25
	interiorWideFrac = 0.15
	maxStrokeFrac    = 0.25
)

// strokeWidth estimates a candidate's border thickness in pixels, as the
// longest run of ink reaching inwards from the middle of any of its four
// sides. The midpoint of a side is where a tick or a cross is least likely to
// touch, so what the run measures is the border itself.
func strokeWidth(ink gocv.Mat, r image.Rectangle) int {
	limit := max(1, int(math.Round(maxStrokeFrac*float64(max(r.Dx(), r.Dy())))))
	run := func(at func(i int) (x, y int)) int {
		n := 0
		for i := range limit {
			x, y := at(i)
			if x < 0 || y < 0 || x >= ink.Cols() || y >= ink.Rows() || ink.GetUCharAt(y, x) == 0 {
				break
			}
			n++
		}
		return n
	}
	midX, midY := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	return max(
		max(run(func(i int) (int, int) { return midX, r.Min.Y + i }),
			run(func(i int) (int, int) { return midX, r.Max.Y - 1 - i })),
		max(run(func(i int) (int, int) { return r.Min.X + i, midY }),
			run(func(i int) (int, int) { return r.Max.X - 1 - i, midY })),
	)
}

// interiorInk is the fraction of inked pixels in the middle of a candidate.
//
// It deliberately measures only the central half, not everything inside the
// border. A border's own anti-aliasing bleeds a pixel or two inwards, which is
// enough to put the interior of a 28px box at 0.135 ink while a thin tick mark
// in the same box reads 0.135 too. The middle stays clean: across a real form
// the measure comes out bimodal, 0.00 for empty boxes and 0.30-0.40 for marked
// ones, and a thin cross measures 0.26 against an empty box's 0.00.
//
// Both windows are inset past the measured border rather than by the fraction
// alone. A fixed fraction only clears the stroke on a big box: at MinSide a
// 3px border is more than 15% of the side, so the wide window would contain
// the border and read an empty box at 0.36 ink - well past CheckedWideInk,
// and reported as marked.
func interiorInk(ink gocv.Mat, r image.Rectangle) (central, wide float64, ok bool) {
	stroke := strokeWidth(ink, r)
	measure := func(frac float64) (float64, bool) {
		dx := inset(frac, r.Dx(), stroke)
		dy := inset(frac, r.Dy(), stroke)
		in := image.Rect(r.Min.X+dx, r.Min.Y+dy, r.Max.X-dx, r.Max.Y-dy).
			Intersect(image.Rect(0, 0, ink.Cols(), ink.Rows()))
		if in.Dx() < 1 || in.Dy() < 1 {
			return 0, false
		}
		roi := ink.Region(in)
		defer roi.Close()
		return float64(gocv.CountNonZero(roi)) / float64(in.Dx()*in.Dy()), true
	}

	central, ok = measure(interiorFrac)
	if !ok {
		return 0, 0, false
	}
	wide, ok = measure(interiorWideFrac)
	return central, wide, ok
}

// inset is how far in from one edge a measurement window starts: the larger of
// the requested fraction and one pixel clear of the border, but never so far
// that nothing is left to measure - an unmeasurable candidate is dropped
// entirely, which is worse than reading a cramped window.
func inset(frac float64, dim, stroke int) int {
	n := max(int(math.Round(frac*float64(dim))), stroke+1)
	return min(n, max((dim-1)/2, 0))
}

// edgeCoverage returns the weakest of the four sides of r, each measured as the
// fraction of that side's length carrying ink within a thin band of the edge.
// A ruled box covers all four sides end to end, even with nicks in the stroke.
// A letter does not: the top and bottom of an "H" are empty between its stems.
func edgeCoverage(ink gocv.Mat, r image.Rectangle) (float64, bool) {
	r = r.Intersect(image.Rect(0, 0, ink.Cols(), ink.Rows()))
	if r.Dx() < 3 || r.Dy() < 3 {
		return 0, false
	}
	band := max(1, int(math.Round(0.12*float64(max(r.Dx(), r.Dy())))))

	// Reducing a band to its brightest value per column, or per row, says
	// which columns and rows carry any ink at all near that edge.
	strip := func(sub image.Rectangle, dim int) float64 {
		roi := ink.Region(sub)
		defer roi.Close()

		reduced := gocv.NewMat()
		defer reduced.Close()
		if err := gocv.Reduce(roi, &reduced, dim, gocv.ReduceMax, gocv.MatTypeCV8U); err != nil {
			return 0
		}
		total := reduced.Rows() * reduced.Cols()
		if total == 0 {
			return 0
		}
		return float64(gocv.CountNonZero(reduced)) / float64(total)
	}

	top := image.Rect(r.Min.X, r.Min.Y, r.Max.X, min(r.Min.Y+band, r.Max.Y))
	bottom := image.Rect(r.Min.X, max(r.Max.Y-band, r.Min.Y), r.Max.X, r.Max.Y)
	left := image.Rect(r.Min.X, r.Min.Y, min(r.Min.X+band, r.Max.X), r.Max.Y)
	right := image.Rect(max(r.Max.X-band, r.Min.X), r.Min.Y, r.Max.X, r.Max.Y)

	return min(
		min(strip(top, 0), strip(bottom, 0)),
		min(strip(left, 1), strip(right, 1)),
	), true
}

func inkFraction(ink gocv.Mat) float64 {
	total := ink.Cols() * ink.Rows()
	if total == 0 {
		return 0
	}
	return float64(gocv.CountNonZero(ink)) / float64(total)
}

// toGray flattens img onto a white background, so that a transparent PNG reads
// as paper rather than as solid ink.
func toGray(img image.Image) *image.Gray {
	b := img.Bounds()
	if b.Dx() < 3 || b.Dy() < 3 {
		return nil
	}
	gray := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(gray, gray.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(gray, gray.Bounds(), img, b.Min, draw.Over)
	return gray
}

// blockSize is the adaptive threshold's neighbourhood: big enough to hold a
// checkbox and some surrounding paper, and odd, as OpenCV requires.
func blockSize(w, h int) int {
	n := min(max(min(w, h)/25, 15), 101)
	if n%2 == 0 {
		n++
	}
	return n
}

// dedupe drops candidates that overlap or nest inside another, which is what a
// box's outline and its own interior hole produce. The larger box wins, so a
// hole never shadows the border around it.
func dedupe(dets []detection) []detection {
	sort.SliceStable(dets, func(i, j int) bool {
		a, b := dets[i].Box, dets[j].Box
		if an, bn := a.Width*a.Height, b.Width*b.Height; an != bn {
			return an > bn
		}
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
// nests reports whether one of the boxes is the other's outline or interior
// hole: the same box found twice, once from each side of its border.
//
// Containment alone is not enough to say that. A ruled response cell, or any
// other box that clears the gates, encloses the checkboxes drawn inside it,
// and since dedupe keeps the larger candidate, treating containment as a
// duplicate would delete every real box in that cell. What distinguishes an
// outline from a container is that the two are near-concentric: the gap
// between them is the border stroke, on all four sides.
func nests(a, b box) bool {
	inside := func(inner, outer box) bool {
		left, top := inner.X-outer.X, inner.Y-outer.Y
		right := outer.X + outer.Width - (inner.X + inner.Width)
		bottom := outer.Y + outer.Height - (inner.Y + inner.Height)
		if left < 0 || top < 0 || right < 0 || bottom < 0 {
			return false
		}
		gapX := int(math.Round(nestGapFrac * float64(outer.Width)))
		gapY := int(math.Round(nestGapFrac * float64(outer.Height)))
		return left <= gapX && right <= gapX && top <= gapY && bottom <= gapY
	}
	return inside(a, b) || inside(b, a)
}

// nestGapFrac is how far the two can sit apart on any one side and still be
// the same box seen twice. It matches maxStrokeFrac, the widest border the
// interior measurement will account for.
const nestGapFrac = maxStrokeFrac

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
	sort.Ints(heights)
	rowHeight := max(1, heights[len(heights)/2])

	row := func(d detection) int { return (d.Box.Y + d.Box.Height/2) / rowHeight }
	sort.SliceStable(dets, func(i, j int) bool {
		if ri, rj := row(dets[i]), row(dets[j]); ri != rj {
			return ri < rj
		}
		return dets[i].Box.X < dets[j].Box.X
	})
}
