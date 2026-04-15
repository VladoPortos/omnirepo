package oci_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mintTokenWithExp signs a JWT with the package's identity claims shape
// using the supplied secret and an explicit exp time. Used by tests that
// need to bypass the production issueToken (which controls TTL strictly).
func mintTokenWithExp(t *testing.T, secret []byte, actorID int64, exp time.Time) string {
	t.Helper()
	claims := jwt.MapClaims{
		"actor_id": actorID,
		"kind":     "user",
		"iss":      "omnirepo",
		"sub":      strconv.FormatInt(actorID, 10),
		"iat":      time.Now().Unix(),
		"exp":      exp.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}
