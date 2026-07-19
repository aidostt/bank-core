// Package api embeds the OpenAPI contract served at /v1/openapi.yaml.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPISpec []byte
