package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	// Registered so image.DecodeConfig can read these formats' headers.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	// imageField is the multipart form field holding the upload.
	imageField = "image"

	// maxUploadBytes caps the whole request body; maxMemoryBytes is how much of
	// it multipart parsing keeps in RAM before spilling to a temp file.
	maxUploadBytes = 10 << 20 // 10 MiB
	maxMemoryBytes = 4 << 20  // 4 MiB

	// sniffBytes is what http.DetectContentType needs to identify a format.
	sniffBytes = 512

	// maxPixels caps the decoded image, which bounds both the memory a decode
	// allocates and the time detection spends scanning it. 24M pixels is a
	// 6000x4000 image, about 96 MB once decoded to RGBA.
	maxPixels = 24_000_000

	pdfType = "application/pdf"
)

// allowedTypes are the formats accepted, keyed by the type sniffed from the
// uploaded bytes rather than the client's declared Content-Type.
var allowedTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
	pdfType:      true,
}

// detectResponse is the full answer, returned only when the caller asks for
// it with ?detail=true. Everything in it beyond the boxes is explanation:
// what was uploaded, which of the two paths answered, and how sure the
// detector is.
type detectResponse struct {
	RequestID string    `json:"request_id,omitempty"`
	Image     imageInfo `json:"image"`
	// Method is how the checkboxes were found: "acroform" when read from a
	// PDF's own form fields, "pixels" when found by the image detector.
	Method string      `json:"method"`
	Boxes  []detection `json:"boxes"`
}

// briefResponse is the default: where the checkboxes are and which are
// marked, which is the question the endpoint was asked.
type briefResponse struct {
	Boxes []briefDetection `json:"boxes"`
}

// briefDetection is a detection without the detector's own bookkeeping.
// Confidence belongs with the metadata: it describes how the answer was
// arrived at, not what the answer is.
type briefDetection struct {
	Box     box    `json:"bbox"`
	Checked bool   `json:"is_checked"`
	Page    int    `json:"page,omitempty"`
	Name    string `json:"name,omitempty"`
}

// detailParam asks for the full response.
const detailParam = "detail"

// wantsDetail reports whether the request asked for the full response, by
// ?detail=true or a bare ?detail. Any other value is not detail.
func wantsDetail(r *http.Request) bool {
	q := r.URL.Query()
	if !q.Has(detailParam) {
		return false
	}
	if raw := q.Get(detailParam); raw != "" {
		on, err := strconv.ParseBool(raw)
		return err == nil && on
	}
	return true
}

// respondDetections writes the checkboxes in whichever shape was asked for.
func respondDetections(w http.ResponseWriter, r *http.Request, info imageInfo, method string, dets []detection) {
	if wantsDetail(r) {
		writeJSON(w, http.StatusOK, detectResponse{
			RequestID: middleware.GetReqID(r.Context()),
			Image:     info,
			Method:    method,
			Boxes:     dets,
		})
		return
	}

	brief := make([]briefDetection, len(dets))
	for i, d := range dets {
		brief[i] = briefDetection{Box: d.Box, Checked: d.Checked, Page: d.Page, Name: d.Name}
	}
	writeJSON(w, http.StatusOK, briefResponse{Boxes: brief})
}

type imageInfo struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	// Pages is set for PDFs, and counts every page in the document even when
	// only the first maxPDFPages were scanned.
	Pages int `json:"pages,omitempty"`
}

type detection struct {
	Box     box  `json:"bbox"`
	Checked bool `json:"is_checked"`
	// Confidence is 1 when read from a PDF's form fields, and an estimate from
	// the detector's shape scoring otherwise.
	Confidence float64 `json:"confidence"`
	// Page is the 1-based page a PDF detection came from, omitted for images.
	Page int `json:"page,omitempty"`
	// Name is the PDF's fully qualified field name, set only on the AcroForm
	// path: pixels carry no names.
	Name string `json:"name,omitempty"`
}

// box is an axis-aligned bounding box in pixels with the origin at the image's
// top-left. For PDFs the pixels are at rasterDPI (300), so both PDF paths
// agree on coordinates.
//
// It is held as origin plus size, which is what the detector and the PDF
// geometry work in, and serialised as the [x1 y1 x2 y2] corner pair the API
// promises. The second corner is exclusive: x2 is x1+width, so the width is
// x2-x1 and a 1px box is [x, y, x+1, y+1].
type box struct {
	X      int
	Y      int
	Width  int
	Height int
}

func (b box) MarshalJSON() ([]byte, error) {
	return json.Marshal([4]int{b.X, b.Y, b.X + b.Width, b.Y + b.Height})
}

func (b *box) UnmarshalJSON(data []byte) error {
	var corners [4]int
	if err := json.Unmarshal(data, &corners); err != nil {
		return err
	}
	if corners[2] < corners[0] || corners[3] < corners[1] {
		return fmt.Errorf("bbox %v has its corners the wrong way round", corners)
	}
	*b = box{
		X:      corners[0],
		Y:      corners[1],
		Width:  corners[2] - corners[0],
		Height: corners[3] - corners[1],
	}
	return nil
}

// handleDetect serves POST /detect: it takes one image or PDF from the image
// form field and responds with the checkboxes found in it. Each way an upload
// can be rejected maps to its own status code, so clients can tell a file that
// is too big from one that is the wrong type or is corrupt.
func handleDetect(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

	if err := r.ParseMultipartForm(maxMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || strings.Contains(err.Error(), "too large") {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload must be at most %d bytes", int64(maxUploadBytes)))
			return
		}
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("request must be multipart/form-data with an %q file field", imageField))
		return
	}
	// Parsing may have spilled the upload to a temp file; drop it on the way out.
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile(imageField)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing %q file field", imageField))
		return
	}
	defer file.Close()

	contentType, err := sniffType(file)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	info := imageInfo{
		Filename:    filepath.Base(header.Filename),
		ContentType: contentType,
		SizeBytes:   header.Size,
	}

	if contentType == pdfType {
		handlePDF(w, r, file, info)
		return
	}

	cfg, err := imageHeader(file, contentType)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	info.Width, info.Height = cfg.Width, cfg.Height

	if info.Width*info.Height > maxPixels {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("image must be at most %d pixels, got %dx%d",
				maxPixels, info.Width, info.Height))
		return
	}

	img, _, err := image.Decode(file)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "could not decode the uploaded image")
		return
	}

	boxes, err := detector.Detect(img)
	if err != nil {
		// The image was fine; examining it was not. Reporting no checkboxes
		// here would be indistinguishable from a blank form.
		writeError(w, http.StatusInternalServerError, "could not examine the uploaded image")
		return
	}

	respondDetections(w, r, info, methodPixels, boxes)
}

// handlePDF spools the upload to disk, which both pdfcpu and pdftoppm need,
// and reports the checkboxes found in it.
func handlePDF(w http.ResponseWriter, r *http.Request, file io.Reader, info imageInfo) {
	path, cleanup, err := spool(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not buffer the uploaded pdf")
		return
	}
	defer cleanup()

	dets, pages, method, err := detectPDF(r.Context(), path)
	switch {
	case errors.Is(err, errNoRasterizer):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "pdf took too long to process")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "could not read the uploaded pdf")
		return
	}
	info.Pages = pages

	respondDetections(w, r, info, method, dets)
}

// spool copies an upload to a temp file and returns its path.
func spool(file io.Reader) (path string, cleanup func(), err error) {
	tmp, err := os.CreateTemp("", "detect-upload-*.pdf")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}

	if _, err := io.Copy(tmp, file); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

// sniffType identifies the upload from its own bytes, ignoring whatever the
// client declared, and leaves the reader rewound.
func sniffType(file io.ReadSeeker) (string, error) {
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", errors.New("could not read the upload")
	}
	if n == 0 {
		return "", errors.New("uploaded file is empty")
	}

	contentType := strings.Split(http.DetectContentType(head[:n]), ";")[0]
	if !allowedTypes[contentType] {
		return "", fmt.Errorf("unsupported type %q, want one of: %s",
			contentType, strings.Join(sortedTypes(), ", "))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("could not read the upload")
	}
	return contentType, nil
}

// imageHeader reads the pixel dimensions out of an image header, leaving the
// reader rewound. Every accepted image type has a registered decoder, so a
// failure here means the bytes are malformed rather than an unknown format.
func imageHeader(file io.ReadSeeker, contentType string) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return cfg, fmt.Errorf("malformed or truncated %s image", contentType)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return cfg, errors.New("could not read the upload")
	}
	return cfg, nil
}

// sortedTypes lists allowedTypes in a stable order for error messages.
func sortedTypes() []string {
	types := make([]string, 0, len(allowedTypes))
	for t := range allowedTypes {
		types = append(types, t)
	}
	slices.Sort(types)
	return types
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError sends msg as a JSON {"error": msg} body.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
