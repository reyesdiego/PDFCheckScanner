#!/usr/bin/env bash
#
# The checks in api.http, from a shell. Same requests, same expectations, but
# runnable without an IDE and usable in CI: it exits non-zero if anything is
# off, so it can gate a deploy.
#
#   make run          # or make up, in another shell
#   make smoke        # or: scripts/smoke.sh
#   HOST=http://localhost:9000 scripts/smoke.sh
#
# Needs curl and jq. Run it from the repository root; the fixtures it posts
# are the committed ones in testdata/.

set -u

host=${HOST:-http://localhost:8080}
pass=0
fail=0

# check NAME WANT GOT
check() {
	if [ "$2" = "$3" ]; then
		printf 'ok   %s\n' "$1"
		pass=$((pass + 1))
	else
		printf 'FAIL %s\n       want: %s\n        got: %s\n' "$1" "$2" "$3"
		fail=$((fail + 1))
	fi
}

# detect PATH FIXTURE -> the response body
detect() {
	curl -sS -X POST "$host$1" -F "image=@testdata/$2"
}

# status METHOD PATH [curl args...] -> the HTTP status code
status() {
	method=$1
	path=$2
	shift 2
	curl -sS -o /dev/null -w '%{http_code}' -X "$method" "$host$path" "$@"
}

for tool in curl jq; do
	command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[ -f testdata/form.png ] || { echo "run me from the repository root" >&2; exit 2; }
curl -sS -o /dev/null "$host/detect" -X GET || { echo "no server on $host" >&2; exit 2; }

echo "== the shape of the answer"

# The default response is the boxes and nothing else.
check "default response has only boxes" \
	"boxes" \
	"$(detect /detect form.png | jq -r '[keys[]] | join(",")')"

check "a box has only bbox and is_checked" \
	"bbox,is_checked" \
	"$(detect /detect form.png | jq -r '[.boxes[0] | keys[]] | join(",")')"

# ?detail=true puts the metadata back.
check "?detail=true adds the metadata" \
	"boxes,image,method,request_id" \
	"$(detect '/detect?detail=true' form.png | jq -r '[keys[]] | join(",")')"

check "?detail=true adds confidence" \
	"true" \
	"$(detect '/detect?detail=true' form.png | jq -r '.boxes[0].confidence > 0')"

check "a bare ?detail does the same" \
	"boxes,image,method,request_id" \
	"$(detect '/detect?detail' form.png | jq -r '[keys[]] | join(",")')"

check "?detail=perhaps is not a request for detail" \
	"boxes" \
	"$(detect '/detect?detail=perhaps' form.png | jq -r '[keys[]] | join(",")')"

echo "== what the detector finds"

check "form.png: 4 boxes, the middle two marked" \
	"false,true,true,false" \
	"$(detect /detect form.png | jq -r '[.boxes[].is_checked | tostring] | join(",")')"

check "image2.png: 48 boxes, 12 of them marked" \
	"48 12" \
	"$(detect /detect image2.png | jq -r '"\(.boxes | length) \([.boxes[] | select(.is_checked)] | length)"')"

check "image1.png: at least 55 small boxes, the two lost pentagons among them" \
	"true true" \
	"$(detect /detect image1.png | jq -r '.boxes as $b | "\($b | length >= 55) \([[814,169],[711,169]] | all(. as $p | any($b[]; (.bbox[0] - $p[0] | fabs) <= 3 and (.bbox[1] - $p[1] | fabs) <= 3)))"')"

check "image3.png: boxes found, none inside the section banner" \
	"true 0" \
	"$(detect /detect image3.png | jq -r '"\(.boxes | length > 0) \([.boxes[] | select(.bbox[2] <= 36)] | length)"')"

check "image4.png: at least 110 boxes, none inside the section banner" \
	"true 0" \
	"$(detect /detect image4.png | jq -r '"\(.boxes | length >= 110) \([.boxes[] | select(.bbox[2] <= 36)] | length)"')"

check "mixed.pdf: 6 boxes among real text" \
	"false,true,true,false,false,true" \
	"$(detect /detect mixed.pdf | jq -r '[.boxes[].is_checked | tostring] | join(",")')"

check "prose.pdf: not one checkbox on a page of text" \
	"0" \
	"$(detect /detect prose.pdf | jq -r '.boxes | length')"

echo "== the two PDF paths"

check "form-fields.pdf is answered from the AcroForm" \
	"acroform" \
	"$(detect '/detect?detail=true' form-fields.pdf | jq -r '.method')"

check "form-fields.pdf carries its field names" \
	"agree,subscribe,consent.marketing,third" \
	"$(detect /detect form-fields.pdf | jq -r '[.boxes[].name] | join(",")')"

check "scanned-form.pdf falls back to the pixels" \
	"pixels 8 3" \
	"$(detect '/detect?detail=true' scanned-form.pdf | jq -r '"\(.method) \(.boxes | length) \([.boxes[] | select(.is_checked)] | length)"')"

check "unreadable form fields fall through to the rasterizer" \
	"pixels 8" \
	"$(detect '/detect?detail=true' scanned-broken-fields.pdf | jq -r '"\(.method) \(.boxes | length)"')"

check "a WebP upload is decoded" \
	"200" \
	"$(status POST /detect -F "image=@testdata/lossy.webp")"

echo "== the error paths"

check "wrong field name -> 400" \
	"400" \
	"$(status POST /detect -F "photo=@testdata/form.png")"

check "a JSON body instead of an upload -> 400" \
	"400" \
	"$(status POST /detect -H 'Content-Type: application/json' -d '{"image":"form.png"}')"

check "something that is not an image -> 415" \
	"415" \
	"$(status POST /detect -F "image=@go.mod;filename=not-an-image.png")"

check "GET is not routed -> 405" \
	"405" \
	"$(status GET /detect)"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
