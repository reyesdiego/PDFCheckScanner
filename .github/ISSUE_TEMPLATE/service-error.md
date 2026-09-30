---
name: Service error
about: A request failed or returned an unexpected status, or the service would not start
labels: bug
---

<!--
Security problem? Don't describe it here. Open an issue titled
"Security contact" with no details instead.
-->

**What happened, and what you expected**

**The request**

```sh
curl -i -X POST "localhost:8080/detect?detail=true" -F "image=@..."
```

**The response** (status and body)

```http

```

**The server log** around the response's `request_id`
<!-- A 500's cause is only in the log, never in the response. -->

```text

```

**Where it runs**
- Docker or local:
- Commit (`git rev-parse --short HEAD`):
- `pdftoppm` installed (local only):
- `-max-pixels` / `MAX_PIXELS`, if set:
