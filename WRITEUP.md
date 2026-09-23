# Approach, tradeoffs and limitations

## Approach

Two paths, picked per upload.

**If a PDF already knows the answer, ask it.** A PDF with AcroForm checkbox
widgets carries the state in its own form fields. That path reads the field
values with pdfcpu, converts each widget's `/Rect` into pixel coordinates, and
returns `confidence: 1` — there is nothing to estimate. It also returns the
qualified field name, which says *which question* each answer belongs to, and
no pixel detector can recover that. Radio buttons and push buttons are filtered
out by their field flags; a widget's `/AS` appearance state beats an inherited
`/V` when they disagree, because `/AS` is what is actually drawn.

**Otherwise, look at the pixels.** Images, and PDFs that are scanned or
flattened, go through OpenCV (via gocv):

1. Flatten onto white and convert to grayscale, so a transparent PNG reads as
   paper rather than as solid ink.
2. Binarize: Gaussian adaptive threshold, OR-ed with an absolute dark floor.
3. `findContours` with `RetrievalList`, so holes come back alongside outlines.
4. Fit `approxPolyDP` to each contour's **convex hull**. Keep the
   four-cornered ones.
5. Filter on size, squareness, how much of its bounding box the hull encloses,
   and how completely ink runs along each of the four sides.
6. Decide marked or not from the ink in the **central half** of the box.
7. Deduplicate on overlap and nesting, then sort into reading order.

A PDF with no form fields is rasterized by `pdftoppm` at 300 DPI first. A PDF
whose fields pdfcpu *rejects* as invalid falls through to the same pixel path
rather than failing the request — that happens with real files, and a malformed
text field should not cost you the checkboxes.

## Why quadrilaterals, and not square blobs of ink

The first version of this detector was hand-written Go: Sauvola thresholding,
connected-component labelling, then geometric scoring of each blob — aspect
ratio, how completely the bounding box edges were covered, how empty the middle
was. It passed every test I had written and was badly wrong.

On the challenge's own description PDF it returned **2,309 boxes**, including
**167 on page 1 and 82 on page 2**, which are pages of prose containing no
checkboxes whatsoever. Drawing the detections onto the page showed it boxing
letters: `B`, `a`, `n`, `o`, `e`, `D`, `H`.

The instructive case is `D`. It has a straight left stem, straight top and
bottom strokes, a right side whose curve still covers its bounding-box edge,
and a counter — the hole — that measured **0.01 interior ink**, indistinguishable
from a genuinely empty checkbox. By every blob-geometry measure available it
*is* a checkbox. Tightening the thresholds enough to exclude it also excluded
real boxes.

What separates them is that a checkbox is a **quadrilateral** and a letter is
not, and that is what `approxPolyDP` measures directly. The polygon-fitting
tolerance matters more than any other single parameter:

| tolerance (fraction of perimeter) | false positives on 3 prose pages | detections on 4 form pages |
| --- | --- | --- |
| 0.05 | 14 | 305 |
| **0.02**, rectangularity 0.85 | **0** | **283** |
| 0.02, rectangularity 0.90 | 0 | 259 |

At 0.05 the counter of a bold `B` or `D` fits a quad. At 0.02 it does not, and
the prose pages come back empty. Tightening further starts eating real boxes
(page 6 falls from 82 to 53), so 0.02 / 0.85 is the chosen point.

## Hulls, because real marks overflow their boxes

Fitting the polygon to the raw contour is too literal about what a scanned box
looks like. Two failure modes came out of the sample documents:

- **A bold X that pokes out past the border.** Its spikes make the outline
  detour around each one: a box measuring a perfect 37x37 square, squareness
  1.00, came back with **7 corners** and was rejected.
- **A border with a cut in it.** A broken ring encloses no area, so its contour
  doubles back along the stroke: another clean 34x33 square scored
  **rectangularity 0.10** where an intact ring scores ~1.0.

Fitting the hull instead ignores both spikes and nicks, and the hull of a round
letter is still not a quadrilateral, so the discrimination survives. A hull is
blind to what is missing from the *middle* of a side, though, and blocky
capitals like `H`, `E` and `N` have rectangular hulls — so each of the four
sides must also carry ink along at least 80% of its length, which the top and
bottom of an `H` do not.

Both thresholds were set by sweeping them across the sample crops and the prose
pages together. At hull 0.85 the bold-X box was still rejected on coverage
(0.81); at hull 0.85 / coverage 0.80 it was found but the cut-border box was
not (hull 0.80, a chamfer where its corner was severed); at **0.80 / 0.80**
both are found. Coverage later came down again, to **0.75**, for a reason worth
recording: the same physical checkbox scored 0.88 coverage in a crop where it
was 33px across and 0.77 in the full page where it was 26px, because each
missing border pixel costs 1/26 of a side instead of 1/33. The measure is
harsher the smaller the box is rendered. Prose false positives stayed at **0**
throughout, and the form pages went from 283 detections to 315.

Contours also solved a recall problem for free. On real appraisal forms a
checkbox's border often runs into the surrounding table rule, so its outline is
welded into the grid and lost. But an unchecked box encloses a hole, and with
`RetrievalList` that hole is a contour in its own right — so the box is still
found by its interior.

## Resolution is a design parameter, not a detail

At 200 DPI the checkboxes on a real appraisal form come out 11-13px, and their
interiors measure **0.55-0.78 ink**: anti-aliasing fills a small box in, so an
*empty* box reads as marked. That is why an early version reported 158 of 167
detections as checked. At 300 DPI the same boxes are 17-19px with interiors of
**0.01-0.11**, which is a measurement you can actually threshold.

So PDFs are rasterized at 300 DPI, and `MinSide` is 12px with the reasoning
written down: below that there is nothing left to measure and the honest answer
is to report nothing rather than to guess.

## Binarization

Global thresholding does not work on documents. Otsu's method picks the split
that maximizes between-class variance, and on a page where ink covers under 1%
of the pixels, the best such split cuts the *paper noise* in half rather than
separating ink from paper — 169.0 against 153.4 on a page I measured. Half the
page becomes "ink" and the checkboxes drown.

Local thresholding fixes that, with two adjustments found by measurement:

- **A generous offset (30), and no pre-blur.** OpenCV's adaptive threshold at a
  small offset marked **37% of noisy paper as ink**, because paper grain alone
  clears the local mean. Blurring first also fixes it, but it widens every
  stroke and inflates every reported box by a pixel on each side, so the offset
  does the work instead.
- **An absolute dark floor (90), OR-ed in.** Adaptive thresholding hollows out
  large solid areas, since the middle of a filled blob has no local contrast. A
  filled mark would become a ring, and then a bogus empty checkbox.

## Marked or unmarked

Measured over **two windows**, and marked if either says so.

A border's own anti-aliasing bleeds a pixel or two inwards. On a 28px box with
thin cross marks that put the interior at 0.135 ink against a 0.15 cutoff, so
both marked boxes reported `is_checked: false`. The middle of the box stays
clean: the same marks measure **0.255** there, against **0.000** for the empty
ones. Across a real form the measure is cleanly bimodal — 0.00 for 38 empty
boxes, 0.30-0.40 for 12 marked ones — so any threshold between 0.05 and 0.25
gives the same answer. It is set at 0.10, in the middle of a wide gap rather
than skimming the edge of a narrow one.

The central half alone is not enough, though, because some marks leave their
ink in the corners. A worn or scratchy X puts blobs at the four corners of the
box and almost nothing in the middle, and the central half then reads it as
empty — and reads it *differently* depending on the scale the box is rendered
at. One such box measured 0.066 in a full page and 0.114 in a crop of the same
page, landing on opposite sides of the threshold; over a window inset only 15%
it measured 0.153 and 0.156, stable to within a rounding error. Across 43
detections in one document and 315 in another, genuinely empty boxes never
exceeded 0.12 on that wider window, so a mark is now anything above 0.10 in
the middle **or** 0.13 in the wider window.

## A minimum on the blended score

A hand-drawn square is geometrically a checkbox, and it clears every individual
gate — hull 0.81 against a bar of 0.80, coverage 0.76 against 0.75 — where a
printed box in the same row scores 0.94 and 1.00. No single threshold rejects
it without also rejecting real boxes, because the real marginal cases are
marginal on *one* axis each: the box with a severed corner scores hull 0.80 but
coverage 0.91, and the small one scores coverage 0.77 but hull 0.91.

Folding coverage into the confidence and requiring a minimum of 0.87 separates
them: the hand-drawn square scores 0.83, the two marginal real boxes 0.90 and
0.91, printed boxes 0.98. It also removed seven text false positives from the
sample documents — letters from "cond" and "average", each a 10-16px
near-square blob that had been scraping past the individual gates.

## Results

On the challenge's description PDF, whose pages 1, 2 and 7 are prose and whose
pages 3-6 are the four sample appraisal documents:

| | prose pages (1, 2, 7) | form pages (3, 4, 5, 6) |
| --- | --- | --- |
| hand-written geometry | 167 / 82 / 195 | 17 / 83 / 24 / 79 |
| contour fit on raw outlines | 0 / 0 / 0 | 121 / 40 / 50 / 72 |
| **current, hull fit** | **0 / 0 / 0** | **133 / 41 / 56 / 78** |

On one region of page 5 where I established ground truth by eye, every checkbox
is found with the correct marked state and no letters are boxed.

Detection costs 37ms on a 3.8MP page and 252ms at the 24MP cap (Apple M4 Pro).
The whole 7-page PDF takes about 4.2s end to end, nearly all of it `pdftoppm`.

## Tradeoffs

**OpenCV through cgo, rather than pure Go.** This costs a system dependency, a
cgo build, and a heavier deploy image; a static pure-Go binary would be nicer to
ship. But `approxPolyDP` and contour hierarchies are exactly what this problem
needs, and hand-rolling them well is a lot of subtle code. The measured
difference — 2,309 detections against 283, and 167 false positives on a prose
page against 0 — settled it.

**Classical CV, rather than a trained model.** No labelled data came with the
challenge, and a geometric detector is deterministic, fast, needs no GPU, and
explains itself: every rejection traces to a named threshold with a measurement
behind it. A model would handle handwriting, skew and unusual box styles far
better, and would be the right next step given labels.

**`pdftoppm` as a subprocess, rather than linking a PDF renderer.** Go has no
PDF rasterizer, and no mature pure-Go one exists. The alternative, MuPDF
bindings through cgo, is AGPL — which matters for commercial use — so the
external binary is the cleaner dependency. It is also only needed for PDFs
without form fields.

**Exclusive bottom-right corner.** The spec says "top-left and bottom-right
corners" without settling whether that pixel is included. Exclusive makes
`width == x2 - x1`, which is the prevailing convention.

**Extra response fields, and where they live.** `page` and `name` are additive
to the specified structure and always present, because a caller cannot
recover either one otherwise. Everything else the service knows -
`confidence`, `method`, the `image` block, `request_id` - describes how the
answer was reached rather than what it is, so it is behind `?detail=true`.
The default response is the specified structure and nothing more, which keeps
the common case exactly what was asked for while leaving the diagnostics one
query parameter away.

## Known limitations

- **Skew.** Boxes are assumed roughly axis-aligned. A photographed or crookedly
  scanned page will start losing boxes, because a rotated square's contour
  still fits a quad but its bounding box no longer matches it, which fails the
  rectangularity test. There is no deskew step.
- **Small boxes.** Below about 12px a box and a letter are both blobs. Scans
  want to be 200-300 DPI; a 72 DPI image will do poorly and the service does
  not warn you.
- **Dark ink on lighter paper only.** An image that is more than 60% ink is
  skipped and returns no boxes rather than a guess. Inverted documents are not
  handled.
- **Square table cells.** A cell that is checkbox-sized and square is a
  quadrilateral with an empty middle, and will be reported. None of the sample
  documents had any, but the failure mode is real.
- **Thresholds are scale-sensitive.** Edge coverage and the adaptive
  threshold's window both depend on how large the box is rendered, and the
  window additionally scales with the image's shorter edge. A form row cropped
  out of a page is therefore not read identically to the same row inside the
  page: one marked box was found in the crop and missed in the full image until
  the coverage gate came down to 0.75. Normalising scale, by estimating box
  size in a first pass and resampling, would remove a whole class of these.
- **Size bounds mix an absolute floor with a relative cap, and that is a
  judgement call.** A checkbox side is capped at the larger of 120px, roughly a
  centimetre at the 300 DPI that PDFs are rasterized at, and 6% of the image's
  shorter edge, which is what keeps a high-DPI scan readable: a 16pt box at 600
  DPI is ~133px and a purely absolute cap threw it out, returning nothing at
  all for a perfectly good page. It is also capped at 90% of the shorter edge
  so a tiny crop cannot report itself as one big checkbox. The cap used to be
  purely relative, 50% of the shorter edge, which silently broke on a single
  form row cropped out of a page: a 1954x58 strip capped detection at 29px and
  rejected all six of its 33px checkboxes. Neither bound alone works, so the
  floor covers the crop and the relative part covers the resolution.
- **Badly broken borders.** A cut border is tolerated down to a hull filling
  80% of its bounding box and 80% coverage per side, which covers the two real
  cases in the samples. Worse breakage still fails, and the fix for that is
  rule removal plus a local closing rather than looser thresholds — a
  morphological closing applied globally was measured and made things worse,
  welding boxes to nearby rules and destroying marked-state detection
  elsewhere (one crop went from 16 marked boxes to 0).
- **Marks that miss the middle.** Ink in the corners is handled by the wider
  window, but a very light pencil mark can still read as unmarked.
- **A box with a stroke drawn through it is lost.** A pen stroke crossing a
  form welds every box it touches into one shape: a checkbox with a handwritten
  diagonal through it came out as part of a single 648x202 contour and was
  rejected for being far too big. This one is structural, not a threshold:
  erasing the stroke cuts the box's ring at the two corners the stroke passes
  through, leaving disconnected arcs rather than a box with a notch. Five
  approaches were measured and none worked - a morphological closing (welds
  boxes to rules, and destroyed marked-state detection elsewhere), accepting
  broken rings on edge coverage alone (438 false positives on prose), and
  Hough-based stroke removal in three variants (global, unioned with the
  untouched pass, and restricted to non-axis-aligned strokes; the last is safe
  but recovers nothing here). What would work is assembling a rectangle from
  the surviving side fragments, or a learned classifier over candidate regions,
  and both are beyond threshold tuning.
- **Hand-drawn boxes.** One hand-drawn square is rejected by the confidence
  minimum, but that is a threshold on overall quality, not a test for
  handwriting. A neater hand-drawn box would pass, and a printed box on a poor
  scan could fail. Telling print from handwriting properly means measuring
  stroke-width variance and how straight each side is, which is not
  implemented.
- **Recall is only spot-checked.** The 121 / 40 / 50 / 72 counts are plausible
  and the page-5 region was verified by eye, but there are no ground-truth
  labels, so precision and recall are not actually quantified. This is the
  biggest gap in my confidence about the numbers above.
- **Page cap.** Only the first 10 pages of a PDF are scanned, though `pages`
  reports the true total. A page with an outsized MediaBox is rendered again at
  a lower resolution that fits, with its boxes scaled back to the 300 DPI
  coordinate space the other pages report in, so no page is silently dropped.
  The size check still happens after the first render rather than before, so
  `pdftoppm`'s own memory use on that first pass is bounded only by the 30s
  request timeout, and the page costs two renders.
- **Truncation past the header.** A truncated image whose header survives
  returns 200 with whatever decoded, because dimensions are read from the
  header rather than by decoding twice.
- **Operationally bare.** No authentication, no rate limiting, no metrics, no
  tracing, single process, no container. Fine for a review, not for production.
- **Confidence is heuristic.** It is a weighted blend of rectangularity,
  squareness and how far the interior sits from the marked/unmarked boundary.
  It ranks candidates sensibly — real boxes scored 0.98 where letter false
  positives scored 0.86-0.89 — but it is not a calibrated probability.

## What I would do next, in order

1. **Label the four sample pages** and compute precision and recall properly.
   Every tuning decision above was made against counts and eyeballed overlays,
   which is enough to catch gross errors and not enough to tune confidently.
2. **Deskew** before detection, via the dominant angle of the page's long
   lines. This is the limitation most likely to bite on real photographed
   documents.
3. **Morphological rule removal** — erase long horizontal and vertical runs
   before finding contours — so that boxes welded to table rules are found by
   their outline and not only by their hole.
4. **A small classifier on candidate crops.** The geometry stage is a good
   region proposer; a lightweight CNN over 32x32 crops would decide
   checkbox-or-not and marked-or-not far better than thresholds, given labels.
5. **Calibrate confidence** against those labels so it can be thresholded by
   callers.

## Testing notes

Fixtures are generated by `testdata/gen_fixtures.py` rather than committed as
opaque binaries, so they can be inspected and regenerated.

One testing lesson is worth recording, because it is why the false-positive bug
survived so long: every fixture was rectangles on blank paper. The suite was
green while the detector reported 167 checkboxes on a page of prose, because
nothing in `testdata` contained any **text**. `prose.pdf` and `mixed.pdf` now
do — real Helvetica rendered by `pdftoppm`, since hand-drawn approximations of
`B`, `D` and `0` are not convincing enough to reproduce the failure. Both were
checked against the old loose tolerance to confirm they fail on the regression:
`prose.pdf` reports 5 false positives, `mixed.pdf` reports 11 instead of 6.
