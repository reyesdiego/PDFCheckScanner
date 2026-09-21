//go:build samples

// Tests over the uncommitted sample documents; see detector_samples_test.go
// for why they are behind a build tag.

package main

import (
	"context"
	"os"
	"testing"
)

// The challenge description PDF guards the bug that motivated the OpenCV
// port: pages 1, 2 and 7 are prose with no checkboxes on them at all, and the
// geometry detector used to report 167, 82 and 195.
func TestDetectPDFNoFalsePositivesOnProsePages(t *testing.T) {
	const doc = "testdata/homevision.pdf"
	if _, err := os.Stat(doc); err != nil {
		t.Fatalf("%v (samples are not committed; see testdata/README.md)", err)
	}
	requireRasterizer(t)

	dets, pages, method, err := detectPDF(context.Background(), doc)
	if err != nil {
		t.Fatalf("detectPDF: %v", err)
	}
	if method != methodPixels || pages != 7 {
		t.Fatalf("method %q over %d pages, want %q over 7", method, pages, methodPixels)
	}

	prose, samples := 0, 0
	for _, d := range dets {
		switch d.Page {
		case 1, 2, 7:
			prose++
		default:
			samples++
		}
	}
	if prose != 0 {
		t.Errorf("%d detections on pages that contain no checkboxes", prose)
	}
	// The sample appraisal pages are dense with checkboxes; a collapse here
	// would mean the thresholds have been tightened into uselessness.
	if samples < 100 {
		t.Errorf("only %d detections across the four form pages, want 100+", samples)
	}
}
