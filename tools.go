//go:build tools
// +build tools

// Package tools pins dependencies that are referenced by the STACK.md version
// lock but not yet consumed from production code. Keeping the blank imports
// under the `tools` build tag means `go mod tidy` + `go mod vendor` preserve
// the pins without adding the libraries to the default build graph.
// Downstream plans remove individual entries as they start importing the
// packages directly.
package tools

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/go-chi/chi/v5/middleware"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/google/uuid"
	_ "github.com/knadh/koanf/parsers/yaml"
	_ "github.com/knadh/koanf/providers/env/v2"
	_ "github.com/knadh/koanf/providers/file"
	_ "github.com/knadh/koanf/providers/structs"
	_ "github.com/knadh/koanf/v2"
)
