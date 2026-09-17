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
	maxUploadBytes = 10 << 20
	maxMemoryBytes = 4 << 20

	// sniffBytes is what http.DetectContentType needs to identify a format.
	sniffBytes = 512

	// maxPixels caps the decoded image, which bounds both the memory a decode
	// allocates and the time detection spends scanning it.
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

type detectResponse struct {
	RequestID string    `json:"request_id,omitempty"`
	Image     imageInfo `json:"image"`
	// Method is how the checkboxes were found: "acroform" when read from a
	// PDF's own form fields, "pixels" when found by the image detector.
	Method     string      `json:"method"`
	Detections []detection `json:"detections"`
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
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Box        box     `json:"box"`
	Checked    bool    `json:"checked"`
	// Page is the 1-based page a PDF detection came from, omitted for images.
	Page int `json:"page,omitempty"`
	// Name is the PDF's fully qualified field name, set only on the AcroForm
	// path: pixels carry no names.
	Name string `json:"name,omitempty"`
}

// box is an axis-aligned bounding box in pixels, origin at the top-left. For
// PDFs the pixels are at rasterDPI, so both PDF paths agree on coordinates.
type box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

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

	writeJSON(w, http.StatusOK, detectResponse{
		RequestID:  middleware.GetReqID(r.Context()),
		Image:      info,
		Method:     methodPixels,
		Detections: detector.Detect(img),
	})
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

	writeJSON(w, http.StatusOK, detectResponse{
		RequestID:  middleware.GetReqID(r.Context()),
		Image:      info,
		Method:     method,
		Detections: dets,
	})
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

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
