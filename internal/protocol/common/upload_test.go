package common_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/common"
	"github.com/vladoportos/omnirepo/internal/storage"
)

func TestStageBody_StreamsAndHashes(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("artifact-bytes ", 1024)
	r := httptest.NewRequest("PUT", "/p/rpm/r/packages/x.rpm", strings.NewReader(body))
	w := httptest.NewRecorder()

	st, ok := common.StageBody(w, r, root, "rpm", "rpm-upload-*.rpm", "x.rpm", 1<<20)
	if !ok {
		t.Fatalf("StageBody failed: %d %s", w.Code, w.Body.String())
	}
	defer os.Remove(st.TmpPath)

	if st.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", st.Size, len(body))
	}
	wantSum := sha256.Sum256([]byte(body))
	if st.Sum256 != hex.EncodeToString(wantSum[:]) {
		t.Errorf("sum mismatch")
	}
	got, err := os.ReadFile(st.TmpPath)
	if err != nil || string(got) != body {
		t.Errorf("tmp file content mismatch (err=%v)", err)
	}
	if !strings.HasPrefix(st.TmpPath, filepath.Join(root, ".tmp-rpm-uploads")) {
		t.Errorf("tmp file outside expected dir: %s", st.TmpPath)
	}
}

func TestStageBody_TooLarge(t *testing.T) {
	root := t.TempDir()
	r := httptest.NewRequest("PUT", "/x", strings.NewReader(strings.Repeat("a", 2048)))
	w := httptest.NewRecorder()

	_, ok := common.StageBody(w, r, root, "helm", "helm-upload-*.tgz", "c.tgz", 100)
	if ok {
		t.Fatalf("StageBody accepted an over-cap body")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
	// Error paths must not leak temp files.
	ents, _ := os.ReadDir(filepath.Join(root, ".tmp-helm-uploads"))
	if len(ents) != 0 {
		t.Errorf("temp file leaked on error: %v", ents)
	}
}

func TestPromoteStaged(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "staged")
	if err := os.WriteFile(tmp, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	ps := storage.NewPathStore(root)
	r := httptest.NewRequest("PUT", "/x", nil)
	w := httptest.NewRecorder()

	if !common.PromoteStaged(w, r, ps, "rpm", "p/rpm/r/packages/x.rpm", tmp, "x.rpm") {
		t.Fatalf("PromoteStaged failed: %d %s", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "p", "rpm", "r", "packages", "x.rpm"))
	if err != nil || string(got) != "payload" {
		t.Errorf("promoted content mismatch (err=%v)", err)
	}

	// Missing tmp file → 500, false.
	w2 := httptest.NewRecorder()
	if common.PromoteStaged(w2, r, ps, "rpm", "k", filepath.Join(root, "absent"), "x.rpm") {
		t.Errorf("PromoteStaged succeeded with missing tmp file")
	}
	if w2.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w2.Code)
	}
}
