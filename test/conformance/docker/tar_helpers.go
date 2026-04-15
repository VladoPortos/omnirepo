//go:build conformance

package docker

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// buildEmptyLayer writes a tar archive containing a single zero-byte file
// and returns its on-disk path. Used as the layer input for `crane append`.
func buildEmptyLayer(t *testing.T) string {
	t.Helper()
	return buildLayerWithSize(t, 0)
}

// buildLargeLayer writes a tar archive with a single file of the given
// size, returning its on-disk path.
func buildLargeLayer(t *testing.T, size int) string {
	t.Helper()
	return buildLayerWithSize(t, size)
}

func buildLayerWithSize(t *testing.T, size int) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := bytes.Repeat([]byte{'x'}, size)
	hdr := &tar.Header{
		Name:    "content.bin",
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: time.Unix(0, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/layer.tar"
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// exchangeBasicForBearer performs the Docker token exchange against
// /v2/token using the fixture's super-admin Basic credentials and returns
// the bearer token string.
func exchangeBasicForBearer(t *testing.T, f *bootFixture) string {
	t.Helper()
	url := "http://" + f.host + "/v2/token"
	req, _ := http.NewRequest("GET", url, nil)
	basic := base64.StdEncoding.EncodeToString([]byte(f.adminLogin + ":" + f.adminPassword))
	req.Header.Set("Authorization", "Basic "+basic)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v2/token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /v2/token: status=%d body=%s", resp.StatusCode, string(body))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /v2/token response: %v", err)
	}
	if payload.Token == "" {
		t.Fatalf("/v2/token returned empty token")
	}
	return payload.Token
}

// Ensure fmt import is used when no other file references it (keeps
// go vet happy under the conformance build tag).
var _ = fmt.Sprintf
