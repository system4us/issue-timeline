// Package api holds the HTTP API contract. openapi.yaml is compiled into the binary, so the server can serve it
// (GET /api/openapi.yaml) and the tests can validate its responses against it.
package api

import _ "embed"

// OpenAPI is api/openapi.yaml as it is in the source tree.
//
//go:embed openapi.yaml
var OpenAPI []byte
