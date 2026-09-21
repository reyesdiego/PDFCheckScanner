# The detector is cgo against OpenCV and the PDF fallback shells out to
# pdftoppm, so the image carries both. Debian trixie packages OpenCV 4.10 and
# poppler, which saves building OpenCV from source; gocv only needs the 4.x
# API surface used here.
#
# Two stages: the builder needs the OpenCV headers and the Go toolchain, the
# runtime needs only the shared libraries and poppler.

FROM golang:1.27-trixie AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
        libopencv-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Dependencies first, so editing source does not re-download the module cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -o /out/api .

FROM debian:trixie-slim

# poppler-utils is pdftoppm. The libopencv-* runtime packages are the ones the
# detector links against; the -dev packages and their headers stay behind in
# the build stage. The list looks excessive for a program that only thresholds
# and finds contours, but gocv's root package wraps every module, so the
# binary links them all - `ldd` on it names each one. That, not the program,
# is what makes the image about a gigabyte.
RUN apt-get update && apt-get install -y --no-install-recommends \
        poppler-utils \
        libopencv-core410 \
        libopencv-imgproc410 \
        libopencv-imgcodecs410 \
        libopencv-highgui410 \
        libopencv-videoio410 \
        libopencv-calib3d410 \
        libopencv-features2d410 \
        libopencv-objdetect410 \
        libopencv-photo410 \
        libopencv-video410 \
        curl \
    && rm -rf /var/lib/apt/lists/*

# Nothing is written to disk except the temp files each upload spools, so the
# server has no business running as root.
RUN useradd --system --create-home --uid 10001 api
USER api

COPY --from=build /out/api /usr/local/bin/api

EXPOSE 8080
ENTRYPOINT ["api"]
CMD ["-addr", ":8080"]
