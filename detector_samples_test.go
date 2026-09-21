//go:build samples

// The hm*.png crops and homevision.pdf are sample appraisal documents that
// came with the challenge. They are client paperwork, so they are not
// committed, and the tests that read them are behind the `samples` build tag:
//
//	go test -tags samples ./...
//	make samples
//
// The tag is what keeps the gap honest. These cases used to live in the main
// suite and skip when the files were absent, which meant every machine but
// mine ran a green suite that had exercised none of the real-document
// behaviour they were written for. A missing file is now a failure, and a run
// without the tag never claims to have covered them in the first place.

package main

import (
	"image"
	"image/png"
	"os"
	"slices"
	"testing"
)

// loadSample reads one of the uncommitted sample documents.
func loadSample(tb testing.TB, file string) image.Image {
	tb.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		tb.Fatalf("%v (samples are not committed; see testdata/README.md)", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		tb.Fatal(err)
	}
	return img
}

// These are regression floors from measured behaviour, not hand-labelled
// ground truth: they exist to catch a collapse in recall, and only hm4, hm6
// and hm8 have been checked box-by-box against the image.
func TestDetectSampleCrops(t *testing.T) {
	cases := []struct {
		file                 string
		minBoxes, minChecked int
		note                 string
	}{
		{"hm1.png", 100, 30, ""},
		{"hm2.png", 40, 15, ""},
		{"hm3.png", 4, 2, ""},
		{"hm4.png", 1, 1, "border with a severed corner and a scratchy mark"},
		{"hm5.png", 24, 10, ""},
		{"hm6.png", 4, 2, ""},
		{"hm7.png", 3, 1, ""},
		{"hm8.png", 2, 1, "bold X overflowing its box"},
		{"hm9.png", 6, 3, ""},
		{"hm10.png", 6, 2, "a form row cropped to 58px tall"},
		// Two empty boxes, of which only the right-hand one is found: a pen
		// stroke runs diagonally across the row and through the other box's
		// border, which is the table-rule problem in another guise.
		{"hm11.png", 1, 0, "one of two boxes is lost to a stroke across its border"},
	}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got := mustDetect(t, NewDetector(), loadSample(t, c.file))
			checked := 0
			for _, d := range got {
				if d.Checked {
					checked++
				}
			}
			if len(got) < c.minBoxes || checked < c.minChecked {
				t.Errorf("%d boxes (%d checked), want at least %d (%d checked) %s",
					len(got), checked, c.minBoxes, c.minChecked, c.note)
			}
		})
	}
}

// hm10 is one row cut out of hm5 and saved at a larger scale, so the two must
// be read the same way. They were not: the fourth box along, a marked one,
// came out 26px in the full image against 33px in the crop, and at 26px it
// scored 0.77 edge coverage against a 0.80 gate while the crop scored 0.88.
// Each missing border pixel costs 1/26 of a side instead of 1/33, so the same
// physical defect is judged more harshly the smaller the box is rendered.
func TestDetectAgreesAcrossScales(t *testing.T) {
	pattern := func(file string, rowTop, rowBottom int) []bool {
		type found struct {
			x       int
			checked bool
		}
		var row []found
		for _, d := range mustDetect(t, NewDetector(), loadSample(t, file)) {
			if d.Box.Y >= rowTop && d.Box.Y <= rowBottom {
				row = append(row, found{d.Box.X, d.Checked})
			}
		}
		slices.SortFunc(row, func(a, b found) int { return a.x - b.x })

		out := make([]bool, len(row))
		for i, r := range row {
			out[i] = r.checked
		}
		return out
	}

	full := pattern("hm5.png", 250, 270)
	crop := pattern("hm10.png", 0, 30)

	want := []bool{false, true, false, true, false, false}
	for _, c := range []struct {
		name string
		got  []bool
	}{{"hm5 row", full}, {"hm10", crop}} {
		if !slices.Equal(c.got, want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, want)
		}
	}
}

// One row of hm2 held three separate defects, all now fixed:
//
//   - a hand-drawn square was reported as a checkbox. It passed every
//     individual gate by a hair (hull 0.81 against 0.80, coverage 0.76 against
//     0.75) where a printed box clears them all at 0.94 and 1.00, so the
//     blended confidence is what rejects it.
//   - the first box, holding a faint scratchy X, was reported unchecked,
//     because that mark's ink sits in the corners and the central half of the
//     box is nearly empty.
//   - that same box read *checked* in hm3, a crop of hm2, since the central
//     half is sensitive to the scale the box is rendered at.
func TestDetectHandlesHandwritingAndCornerMarks(t *testing.T) {
	var row []detection
	for _, d := range mustDetect(t, NewDetector(), loadSample(t, "hm2.png")) {
		if d.Box.Y >= 910 && d.Box.Y <= 935 {
			row = append(row, d)
		}
	}
	slices.SortFunc(row, func(a, b detection) int { return a.Box.X - b.Box.X })

	if len(row) != 6 {
		t.Fatalf("found %d boxes on the row, want 6: %+v", len(row), row)
	}
	for _, d := range row {
		// The hand-drawn square sat at x=1143, between the real boxes at
		// x=1067 and x=1967.
		if d.Box.X > 1100 && d.Box.X < 1900 {
			t.Errorf("hand-drawn square at x=%d reported as a checkbox (conf %.2f)",
				d.Box.X, d.Confidence)
		}
	}
	if !row[0].Checked {
		t.Errorf("first box reported unchecked; its corner-heavy X makes it checked")
	}

	// hm3 is a crop of the same region and must agree on that first box.
	crop := mustDetect(t, NewDetector(), loadSample(t, "hm3.png"))
	slices.SortFunc(crop, func(a, b detection) int {
		if a.Box.Y/30 != b.Box.Y/30 {
			return a.Box.Y - b.Box.Y
		}
		return a.Box.X - b.Box.X
	})
	if len(crop) == 0 {
		t.Fatal("no boxes in hm3")
	}
	if crop[0].Checked != row[0].Checked {
		t.Errorf("hm3 says checked=%v for the first box, hm2 says %v",
			crop[0].Checked, row[0].Checked)
	}
}
