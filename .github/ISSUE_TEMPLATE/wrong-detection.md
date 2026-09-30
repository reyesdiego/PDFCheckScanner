---
name: Wrong detection
about: A checkbox was missed, something else was reported, or is_checked is wrong
labels: bug
---

<!--
Please don't attach documents with personal data. Crop to the region that
goes wrong, redact the rest, or recreate it on a blank page.
Known limitations are listed in the README: skew, boxes under 12px, and
boxes touching a table rule.
-->

**What is wrong**
<!-- e.g. "the third box in the second row is marked but reported unchecked" -->

**The request**

```sh
curl -X POST "localhost:8080/detect?detail=true" -F "image=@..."
```

**The answer, with `?detail=true`**

```json

```

**The file**
<!-- Attach it, or a crop that reproduces the problem. Scan DPI if known. -->

**Where it runs**
- Docker or local:
- Commit (`git rev-parse --short HEAD`):
- `-max-pixels` / `MAX_PIXELS`, if set:
