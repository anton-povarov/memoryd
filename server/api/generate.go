// Package api contains the HTTP contract and generated Echo server bindings.
package api

import _ "embed"

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config oapi-codegen.yaml openapi.yaml

// OpenapiYAML is the checked-in source contract embedded for documentation.
//
//go:embed openapi.yaml
var OpenapiYAML []byte
