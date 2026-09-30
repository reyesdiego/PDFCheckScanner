package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// previewDPI is what a page preview is rendered at: a third of rasterDPI,
	// which is plenty to see a checkbox on screen and keeps a page to about
	// 100 KB. Box coordinates are at rasterDPI, so a preview says how to scale
	// them rather than the caller assuming a third.
	previewDPI = 100

	// maxPreviewPixels bounds one preview. A page with an outsized MediaBox,
	// a plat or an exhibit, is rendered again smaller rather than sent as a
	// picture tens of megapixels big.
	maxPreviewPixels = 4_000_000

	// minPreviewDPI is as small as an outsized page is shrunk. Below this the
	// preview would no longer show anything worth outlining.
	minPreviewDPI = 10
)

// pagePreview is one page of a PDF, drawn so the upload page can outline the
// boxes on it.
type pagePreview struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
	// Scale maps a box's rasterDPI coordinates onto this image: multiply by it.
	Scale float64 `json:"scale"`
	// Image is the page as a JPEG data URL, ready for an img or a canvas.
	Image string `json:"image"`
}

type previewResponse struct {
	Pages []pagePreview `json:"pages"`
}

// handlePreview serves POST /preview: it takes the same upload as /detect and,
// for a PDF, answers with a picture of each page /detect would scan. It exists
// for the upload page, which can draw an image upload itself but not a PDF.
func handlePreview(w http.ResponseWriter, r *http.Request) {
	file, info, done, ok := readUpload(w, r)
	if !ok {
		return
	}
	defer done()

	if info.ContentType != pdfType {
		writeError(w, http.StatusUnsupportedMediaType,
			fmt.Sprintf("previews are for PDFs; got %s, which can be shown as it is", info.ContentType))
		return
	}

	path, cleanup, err := spool(file)
	if err != nil {
		internalError(w, r, "could not buffer the uploaded pdf", err)
		return
	}
	defer cleanup()

	pages, err := renderPreviews(r.Context(), path)
	switch {
	case errors.Is(err, errNoRasterizer):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "pdf took too long to draw")
	case errors.Is(err, errUnreadablePDF):
		writeError(w, http.StatusUnprocessableEntity, "could not read the uploaded pdf")
	case err != nil:
		internalError(w, r, "could not draw the uploaded pdf", err)
	default:
		writeJSON(w, http.StatusOK, previewResponse{Pages: pages})
	}
}

// previewFileNum matches the page number pdftoppm appends to a JPEG it writes.
var previewFileNum = regexp.MustCompile(`-(\d+)\.jpg$`)

// renderPreviews draws the first maxPDFPages pages of the PDF at path, the
// same pages detection scans.
func renderPreviews(ctx context.Context, path string) ([]pagePreview, error) {
	if _, err := exec.LookPath(rasterizer); err != nil {
		return nil, errNoRasterizer
	}

	dir, err := os.MkdirTemp("", "preview-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	if err := renderJPEG(ctx, path, filepath.Join(dir, "page"), previewDPI, 1, maxPDFPages); err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errUnreadablePDF, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "page-*.jpg"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s produced no pages", errUnreadablePDF, rasterizer)
	}

	pages := make([]pagePreview, 0, len(files))
	for _, file := range files {
		m := previewFileNum.FindStringSubmatch(file)
		if m == nil {
			return nil, fmt.Errorf("unexpected %s output %s", rasterizer, file)
		}
		page, _ := strconv.Atoi(m[1])

		p, err := previewPage(ctx, path, dir, file, page)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	// Glob sorts by name, and pdftoppm pads page numbers to one width, but
	// the order the page draws them in should not rest on that.
	slices.SortFunc(pages, func(a, b pagePreview) int { return a.Page - b.Page })
	return pages, nil
}

// previewPage turns one rendered page into a preview, rendering it again
// smaller if it came out bigger than maxPreviewPixels.
func previewPage(ctx context.Context, pdfPath, dir, file string, page int) (pagePreview, error) {
	cfg, err := pageConfig(file)
	if err != nil {
		return pagePreview{}, fmt.Errorf("decode preview of page %d: %w", page, err)
	}

	dpi := previewDPI
	if area := cfg.Width * cfg.Height; area > maxPreviewPixels {
		fit := 0.98 * math.Sqrt(float64(maxPreviewPixels)/float64(area))
		dpi = max(int(math.Floor(previewDPI*fit)), minPreviewDPI)
		prefix := filepath.Join(dir, fmt.Sprintf("small-%d", page))
		if err := renderJPEG(ctx, pdfPath, prefix, dpi, page, page); err != nil {
			return pagePreview{}, err
		}
		smaller, err := filepath.Glob(prefix + "-*.jpg")
		if err != nil || len(smaller) == 0 {
			return pagePreview{}, fmt.Errorf("%s produced no smaller preview of page %d", rasterizer, page)
		}
		file = smaller[0]
		if cfg, err = pageConfig(file); err != nil {
			return pagePreview{}, fmt.Errorf("decode preview of page %d: %w", page, err)
		}
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return pagePreview{}, err
	}
	return pagePreview{
		Page:   page,
		Width:  cfg.Width,
		Height: cfg.Height,
		Scale:  float64(dpi) / rasterDPI,
		Image:  "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

// renderJPEG runs pdftoppm over pages first to last at dpi, writing
// prefix-N.jpg for each.
func renderJPEG(ctx context.Context, pdfPath, prefix string, dpi, first, last int) error {
	cmd := exec.CommandContext(ctx, rasterizer,
		"-jpeg", "-jpegopt", "quality=80",
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(first),
		"-l", strconv.Itoa(last),
		pdfPath, prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s: %w: %s", rasterizer, err, strings.TrimSpace(string(out)))
	}
	return nil
}
