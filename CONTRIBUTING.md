# Contributing

Bug reports, wrong answers and pull requests are all welcome at
**https://github.com/reyesdiego/PDFCheckScanner**.

## Reporting a problem

Open an issue: https://github.com/reyesdiego/PDFCheckScanner/issues/new/choose.
There are two templates:

- **Wrong detection:** a box was missed, something that is not a checkbox was
  reported, or `is_checked` is wrong.
- **Service error:** a request failed, returned a status you did not expect,
  or the service would not start.

Whichever it is, these make an issue quick to act on:

- **The request.** The exact `curl` command, or what your client sends.
- **The full answer.** Repeat the request with `?detail=true`. The `method`
  says which path answered, and `request_id` matches the server log.
- **The server log** around that `request_id`. A 500's cause is only in the
  log, never in the response.
- **How it runs.** Docker or local, the commit (`git rev-parse --short HEAD`),
  and any `-max-pixels` or `MAX_PIXELS` setting.
- **The file, if you can share it.** A wrong detection is usually impossible
  to fix without the page. Check [Limitations](README.md#limitations) first:
  skewed pages, boxes under 12px and boxes on wavy or tilted rules are known.

**Do not attach documents with personal data.** Appraisal reports carry names,
addresses and prices. Crop to the region that goes wrong, redact the rest, or
recreate the layout on a blank page. A crop that reproduces the problem is
more useful than a whole page anyway.

### Security problems

Do not describe a vulnerability in a public issue. Open an issue titled
"Security contact" with no details, and a private channel will be arranged.

## Making a change

Setup is in the [README](README.md#setup-for-local-development): Go 1.27,
`opencv@4` and `poppler`. Then:

1. **Branch from `main`** and keep the change to one concern.
2. **Add a test that fails without the change.** Bugs in this detector come
   from real pages, so a detection fix should come with a fixture that shows
   it (see below).
3. **Run `make check`** (`gofmt`, `go vet`, and the tests under `-race`).
   `make fmt` rewrites files in place, so commit afterwards.
4. **If the service is involved, run `make smoke`** against `make run` or
   `make up`.
5. **Open a pull request** that says what was wrong, how you found it, and
   what the numbers were before and after, e.g. detections on a fixture and
   false positives on `prose.pdf`.

### Changing the detector

Every threshold is a documented field on `Detector`, with its value set in
`NewDetector`, and most comments give the measurement behind the value. Keep
it that way: a changed threshold needs the case that justifies it, written
into the comment, and a check that the false-positive canaries (`prose.pdf`,
`mixed.pdf`) still pass.

### Fixtures and figures

- **Generated fixtures:** change `testdata/gen_fixtures.py`, run
  `make fixtures`, and commit the script and its output together. Do not
  edit the generated PDFs by hand.
- **Expectations move together.** `api.http`, `scripts/smoke.sh` and the
  table in `testdata/README.md` all state what each fixture should produce;
  update all three.
- **Figures:** if the detector's output changes, run `make docs` to redraw
  `docs/img` and check `docs/PIPELINE.md` still describes them.
- **No real documents in the repository.** `.gitignore` already keeps the
  challenge's sample appraisals out; add any new ones there too.

### Documentation

`README.md` covers using and running the service, `WRITEUP.md` the design
decisions and limitations, and `docs/PIPELINE.md` how detection works. A
change in behaviour should update whichever of them describes it, including
the error table in the README if a status or message changes.
