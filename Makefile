# One-time setup. Homebrew's default `opencv` formula is 5.x, which gocv does
# not support, so the versioned one is needed. `pdftoppm` comes from poppler.
#
#   brew install opencv@4 poppler
#   ln -sf /opt/homebrew/opt/opencv@4/lib/pkgconfig/opencv4.pc \
#          /opt/homebrew/lib/pkgconfig/opencv4.pc
#
# That symlink matters: opencv@4 is keg-only, so its opencv4.pc is outside
# pkg-config's default search path, and gocv's cgo directives ask for it by
# name. Without it `go build` fails for anything that has to compile gocv, and
# so does the IDE's own build, which does not inherit a run configuration's
# environment. The export below is a fallback for machines lacking the symlink.
export PKG_CONFIG_PATH := /opt/homebrew/opt/opencv@4/lib/pkgconfig

ADDR ?= :8080

.PHONY: run build test samples race vet fmt fixtures check up down logs

run:            ## start the API
	go run . -addr $(ADDR)

build:
	go build -o bin/api .

# Always the package, never a single file: `go test some_test.go` compiles that
# file alone and fails with "undefined: NewDetector".
test:
	go test ./...

# The sample appraisal documents are not committed, so the tests that read
# them are behind a build tag and fail loudly rather than skipping. Needs
# testdata/hm*.png and testdata/homevision.pdf.
samples:
	go test -tags samples ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Regenerate testdata from testdata/gen_fixtures.py.
fixtures:
	python3 testdata/gen_fixtures.py

# Docker Compose brings its own OpenCV and poppler, so none of the setup at
# the top of this file applies to it.
up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

check: fmt vet race
