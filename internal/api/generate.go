package api

import _ "embed"

//go:generate oapi-codegen -generate types -o types_gen.go -package api openapi.yaml

//go:embed openapi.yaml
var openapiSpec []byte
