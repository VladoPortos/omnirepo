//go:build spike
// +build spike

// Spike-only imports. Built with `go build -tags spike`.
package main

import (
	_ "github.com/ProtonMail/go-crypto/openpgp"
	_ "github.com/go-git/go-git/v6"
	_ "github.com/opencontainers/go-digest"
)
