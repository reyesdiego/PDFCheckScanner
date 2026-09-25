package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	// rasterDPI is what pdftoppm renders at. At 200 DPI the checkboxes on a real
	// appraisal form come out 11-13px, small enough that anti-aliasing fills
	// their interiors and an empty box reads as marked. At 300 they are 17-19px
	// with interiors that measure 0.01-0.11 ink, which is what the detector
	// needs to tell empty from marked.
	rasterDPI = 300

	// minRasterDPI is the floor for the reduced render an outsized page gets.
	// Below this a checkbox is smaller than MinSide and there is nothing to
	// find, so a page that cannot fit maxPixels at this resolution is one the
	// detector could not have read anyway.
	minRasterDPI = 100

	// maxPDFPages bounds how much work one upload can ask for.
	maxPDFPages = 10

	// pdfPoints is the PDF user-space unit: 72 per inch.
	pdfPoints = 72.0

	rasterizer = "pdftoppm"

	// methodAcroForm means the answer came from the PDF's own form fields and
	// is exact; methodPixels means it came from the detector.
	methodAcroForm = "acroform"
	methodPixels   = "pixels"
)

// errNoRasterizer reports that the pdftoppm binary is missing, which is a
// deployment problem rather than a bad request.
var errNoRasterizer = errors.New(rasterizer + " is not installed, so scanned PDFs cannot be rasterized")

// errUnreadablePDF reports that the upload itself is at fault: pdftoppm could
// not render it. Any other failure on the PDF path is the server's problem.
var errUnreadablePDF = errors.New("pdf could not be rendered")

// detectPDF finds the checkboxes in a PDF at path.
//
// A PDF that carries real AcroForm checkbox widgets already knows its own
// answers, so those are read straight out of the form fields. Scanned or
// flattened PDFs have no fields, and fall back to rasterizing each page with
// pdftoppm and running the pixel detector over it.
func detectPDF(ctx context.Context, path string) (dets []detection, pages int, method string, err error) {
	dets, pages, formErr := acroFormCheckboxes(path)
	if formErr == nil && len(dets) > 0 {
		sortPageOrder(dets)
		return dets, pages, methodAcroForm, nil
	}

	// A PDF that pdfcpu refuses to parse can still be perfectly renderable, so
	// a failed field read falls through to the pixels rather than giving up on
	// the document.
	dets, rendered, rasterErr := rasterCheckboxes(ctx, path, pages)
	if rasterErr != nil {
		return nil, pages, "", errors.Join(formErr, rasterErr)
	}
	if pages == 0 {
		pages = rendered
	}
	return dets, pages, methodPixels, nil
}

// acroFormCheckboxes reads checkbox widgets out of the PDF's form fields. It
// returns no detections, and no error, for a PDF that has none.
func acroFormCheckboxes(path string) ([]detection, int, error) {
	pctx, err := api.ReadContextFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read pdf: %w", err)
	}
	if err := pctx.EnsurePageCount(); err != nil {
		return nil, 0, fmt.Errorf("read pdf pages: %w", err)
	}
	pages := pctx.PageCount

	dets := make([]detection, 0)
	for page := 1; page <= min(pages, maxPDFPages); page++ {
		pageDict, _, attrs, err := pctx.PageDict(page, false)
		if err != nil || pageDict == nil {
			continue
		}
		annots, err := pctx.DereferenceArray(pageDict["Annots"])
		if err != nil || len(annots) == 0 {
			continue
		}

		for _, a := range annots {
			widget, err := pctx.DereferenceDict(a)
			if err != nil || widget == nil {
				continue
			}
			det, ok := checkboxFromWidget(pctx, widget, attrs, page)
			if ok {
				dets = append(dets, det)
			}
		}
	}
	return dets, pages, nil
}

// checkboxFromWidget turns one widget annotation into a detection, if it is a
// checkbox rather than a radio button, push button or other field type.
func checkboxFromWidget(pctx *model.Context, widget types.Dict, attrs *model.InheritedPageAttrs, page int) (detection, bool) {
	if sub := widget.NameEntry("Subtype"); sub == nil || *sub != "Widget" {
		return detection{}, false
	}

	field := fieldAttrs(pctx, widget)
	if field.typ != "Btn" || field.flags&flagRadio != 0 || field.flags&flagPushButton != 0 {
		return detection{}, false
	}

	// The widget's appearance state is what is actually drawn on the page, so
	// it beats an inherited /V when the two disagree.
	state := field.value
	if as := widget.NameEntry("AS"); as != nil {
		state = *as
	}

	rect, ok := rectFromArray(pctx, widget["Rect"])
	if !ok {
		return detection{}, false
	}

	return detection{
		// The PDF states the answer outright; there is nothing to be unsure of.
		Confidence: 1,
		Box:        rectToPixels(rect, attrs),
		Checked:    state != "" && state != "Off",
		Page:       page,
		Name:       field.name,
	}, true
}

// Field flags from the PDF spec, table 226: a /Btn field is a push button or a
// radio button rather than a checkbox when these are set.
const (
	flagPushButton = 1 << 16
	flagRadio      = 1 << 15
)

// fieldInfo is what a widget's field chain says about it.
type fieldInfo struct {
	typ   string
	flags int
	value string
	// name is the fully qualified field name: every /T from the root of the
	// field tree down to this widget, joined with dots, as the PDF spec
	// defines it.
	name string
}

// fieldAttrs resolves a widget's field attributes, following the /Parent chain
// because a widget commonly inherits its type, flags, value and name prefix
// from the field it belongs to.
func fieldAttrs(pctx *model.Context, widget types.Dict) fieldInfo {
	const maxDepth = 8

	var info fieldInfo
	var parts []string

	d := widget
	for range maxDepth {
		if info.typ == "" {
			if ft := d.NameEntry("FT"); ft != nil {
				info.typ = *ft
			}
		}
		if info.flags == 0 {
			if ff := d.IntEntry("Ff"); ff != nil {
				info.flags = *ff
			}
		}
		if info.value == "" {
			if v := d.NameEntry("V"); v != nil {
				info.value = *v
			}
		}
		if t, err := d.StringOrHexLiteralEntry("T"); err == nil && t != nil && *t != "" {
			parts = append(parts, *t)
		}

		parent, err := pctx.DereferenceDict(d["Parent"])
		if err != nil || parent == nil {
			break
		}
		d = parent
	}

	// Collected leaf-first, so reverse into root-first order.
	slices.Reverse(parts)
	info.name = strings.Join(parts, ".")
	return info
}

// rectFromArray reads a PDF /Rect array into a normalised rectangle, reporting
// false if it is not four numbers.
func rectFromArray(pctx *model.Context, o types.Object) (*types.Rectangle, bool) {
	arr, err := pctx.DereferenceArray(o)
	if err != nil || len(arr) != 4 {
		return nil, false
	}
	var v [4]float64
	for i, o := range arr {
		f, err := pctx.DereferenceNumber(o)
		if err != nil {
			return nil, false
		}
		v[i] = f
	}
	// A /Rect's corners are not guaranteed to be given lower-left first.
	return types.NewRectangle(min(v[0], v[2]), min(v[1], v[3]), max(v[0], v[2]), max(v[1], v[3])), true
}

// rectToPixels converts a PDF rectangle into the same coordinate space the
// raster path reports: pixels at rasterDPI, with the origin at the page's
// top-left rather than PDF's bottom-left.
func rectToPixels(r *types.Rectangle, attrs *model.InheritedPageAttrs) box {
	var originX, originY, pageW, pageH float64
	rotate := 0
	if attrs != nil && attrs.MediaBox != nil {
		mb := attrs.MediaBox
		originX, originY = mb.LL.X, mb.LL.Y
		pageW, pageH = mb.Width(), mb.Height()
		rotate = ((attrs.Rotate % 360) + 360) % 360
	} else {
		pageW, pageH = r.UR.X, r.UR.Y
	}

	// Flip to a top-left origin, in points.
	x0, x1 := r.LL.X-originX, r.UR.X-originX
	y0, y1 := pageH-(r.UR.Y-originY), pageH-(r.LL.Y-originY)

	// pdftoppm honours /Rotate, so the rendered page is turned relative to PDF
	// coordinates and the box has to turn with it.
	switch rotate {
	case 90:
		x0, y0, x1, y1 = pageH-y0, x0, pageH-y1, x1
	case 180:
		x0, y0, x1, y1 = pageW-x0, pageH-y0, pageW-x1, pageH-y1
	case 270:
		x0, y0, x1, y1 = y0, pageW-x0, y1, pageW-x1
	}

	// Scale the corners and take the extent from them, so a box never drifts
	// from the edges it was rounded to.
	left, right := px(min(x0, x1)), px(max(x0, x1))
	top, bottom := px(min(y0, y1)), px(max(y0, y1))
	return box{
		X:      left,
		Y:      top,
		Width:  max(1, right-left),
		Height: max(1, bottom-top),
	}
}

// px converts PDF points to pixels at rasterDPI, multiplying before dividing
// so that whole-point sizes land on exact pixel counts.
func px(points float64) int {
	return int(math.Round(points * rasterDPI / pdfPoints))
}

// pageFileNum matches the page number pdftoppm appends to each output file,
// which it zero-pads to the width of the last page number.
var pageFileNum = regexp.MustCompile(`-(\d+)\.png$`)

// rasterCheckboxes renders each page with pdftoppm and runs the pixel detector
// over the results.
func rasterCheckboxes(ctx context.Context, path string, pages int) ([]detection, int, error) {
	if _, err := exec.LookPath(rasterizer); err != nil {
		return nil, 0, errNoRasterizer
	}

	dir, err := os.MkdirTemp("", "detect-pdf-*")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)

	last := maxPDFPages
	if pages > 0 {
		last = min(pages, maxPDFPages)
	}
	cmd := exec.CommandContext(ctx, rasterizer,
		"-png",
		"-r", strconv.Itoa(rasterDPI),
		"-f", "1",
		"-l", strconv.Itoa(last),
		path, filepath.Join(dir, "page"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		return nil, 0, fmt.Errorf("%w: %s: %w: %s", errUnreadablePDF, rasterizer, err, strings.TrimSpace(string(out)))
	}

	files, err := filepath.Glob(filepath.Join(dir, "page-*.png"))
	if err != nil {
		return nil, 0, err
	}
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("%w: %s produced no pages", errUnreadablePDF, rasterizer)
	}

	dets := make([]detection, 0)
	rendered := 0
	for _, file := range files {
		page := 0
		if m := pageFileNum.FindStringSubmatch(file); m != nil {
			page, _ = strconv.Atoi(m[1])
		}
		rendered = max(rendered, page)

		pageDets, err := detectPage(ctx, path, dir, file, page)
		if err != nil {
			return nil, 0, err
		}
		dets = append(dets, pageDets...)
	}
	sortPageOrder(dets)
	return dets, rendered, nil
}

// detectPage runs the detector over one rendered page.
//
// A page with an outsized MediaBox rasterizes into something far bigger than
// an uploaded image is allowed to be. Such a page is rendered again on its
// own, at whatever resolution does fit, and the boxes it yields are scaled
// back up: dropping it instead would answer 200 OK with the page silently
// contributing nothing, and at 300 DPI the cutoff falls on paper sizes people
// really do use - a tabloid plat or an ARCH-B exhibit.
//
// TODO: the size check still comes after the fact, so the first render of an
// outsized page has already cost pdftoppm the memory. Reading page dimensions
// up front would bound that too, and would save the second render.
func detectPage(ctx context.Context, pdfPath, dir, file string, page int) ([]detection, error) {
	cfg, err := pageConfig(file)
	if err != nil {
		return nil, fmt.Errorf("decode page %d: %w", page, err)
	}

	scale := 1.0
	if cfg.Width*cfg.Height > maxPixels {
		file, scale, err = renderPageToFit(ctx, pdfPath, dir, page, cfg)
		if err != nil {
			return nil, err
		}
		if file == "" {
			return nil, nil
		}
	}

	return detectPageImage(file, page, scale)
}

// renderPageToFit re-renders one page at the highest DPI at or below
// rasterDPI that keeps it under maxPixels, and returns the new file along
// with the factor mapping its pixels back to rasterDPI. An empty filename
// means the page cannot be made to fit at a resolution the detector could
// still read.
func renderPageToFit(ctx context.Context, pdfPath, dir string, page int, cfg image.Config) (string, float64, error) {
	// Area scales with the square of the DPI. The margin absorbs pdftoppm
	// rounding the pixel dimensions up.
	fit := 0.98 * math.Sqrt(float64(maxPixels)/float64(cfg.Width*cfg.Height))
	dpi := int(math.Floor(rasterDPI * fit))
	if dpi < minRasterDPI {
		return "", 0, nil
	}

	prefix := filepath.Join(dir, fmt.Sprintf("fit-%d", page))
	cmd := exec.CommandContext(ctx, rasterizer,
		"-png",
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		pdfPath, prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", 0, ctxErr
		}
		return "", 0, fmt.Errorf("%s page %d: %w: %s", rasterizer, page, err, strings.TrimSpace(string(out)))
	}

	files, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return "", 0, err
	}
	if len(files) == 0 {
		return "", 0, fmt.Errorf("%s produced no output for page %d", rasterizer, page)
	}

	cfg, err = pageConfig(files[0])
	if err != nil {
		return "", 0, fmt.Errorf("decode page %d: %w", page, err)
	}
	if cfg.Width*cfg.Height > maxPixels {
		return "", 0, nil
	}
	return files[0], float64(rasterDPI) / float64(dpi), nil
}

// pageConfig reads the dimensions of a rendered page without decoding it.
func pageConfig(file string) (image.Config, error) {
	f, err := os.Open(file)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	return cfg, err
}

// detectPageImage detects on a rendered page, multiplying the boxes by scale
// so that every page reports pixels at rasterDPI however it was rendered.
func detectPageImage(file string, page int, scale float64) ([]detection, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode page %d: %w", page, err)
	}

	dets, err := detector.Detect(img)
	if err != nil {
		return nil, fmt.Errorf("detect page %d: %w", page, err)
	}
	for i := range dets {
		dets[i].Page = page
		if scale != 1 {
			dets[i].Box = scaleBox(dets[i].Box, scale)
		}
	}
	return dets, nil
}

// scaleBox multiplies every coordinate of b by scale, rounding to the nearest
// pixel.
func scaleBox(b box, scale float64) box {
	at := func(n int) int { return int(math.Round(float64(n) * scale)) }
	return box{X: at(b.X), Y: at(b.Y), Width: at(b.Width), Height: at(b.Height)}
}

// sortPageOrder puts detections in page order, keeping the per-page reading
// order the detector already established.
func sortPageOrder(dets []detection) {
	if len(dets) < 2 {
		return
	}
	// Stable so that the reading order within a page survives.
	sort.SliceStable(dets, func(i, j int) bool { return dets[i].Page < dets[j].Page })
}
