package httpapi

import (
	"io"
	"net/http"
)

// docsCSP lets the docs page load its script and styles from the one CDN it names, and read the spec from this
// server. Nothing else: no inline script, no other origin.
const docsCSP = "default-src 'none'; script-src https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"font-src https://cdn.jsdelivr.net data:; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// docsPage renders /api/openapi.yaml with Scalar. The spec is named by a data attribute, so the page needs no inline
// script, which the CSP forbids.
const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>issue-timeline API</title>
</head>
<body>
<script id="api-reference" data-url="/api/openapi.yaml"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.25.0"></script>
</body>
</html>
`

func (s *server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", docsCSP)
	_, _ = io.WriteString(w, docsPage)
}
