package scan_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dxc-internal/omnirepo/internal/scan"
)

func TestFakeRunnerImageFIFO(t *testing.T) {
	f := scan.NewFakeRunner()
	r1 := scan.Result{ArtifactName: "first"}
	r2 := scan.Result{ArtifactName: "second"}
	f.QueueImage(r1, nil)
	f.QueueImage(r2, nil)

	got1, err := f.Image(context.Background(), "x")
	if err != nil || got1.ArtifactName != "first" {
		t.Fatalf("1st Image = %+v, %v; want first, nil", got1, err)
	}
	got2, err := f.Image(context.Background(), "x")
	if err != nil || got2.ArtifactName != "second" {
		t.Fatalf("2nd Image = %+v, %v; want second, nil", got2, err)
	}
	_, err = f.Image(context.Background(), "x")
	if !errors.Is(err, scan.ErrNothingQueued) {
		t.Fatalf("3rd Image err = %v, want ErrNothingQueued", err)
	}
}

func TestFakeRunnerImagePropagatesError(t *testing.T) {
	f := scan.NewFakeRunner()
	want := errors.New("boom")
	f.QueueImage(scan.Result{}, want)
	_, got := f.Image(context.Background(), "x")
	if !errors.Is(got, want) {
		t.Errorf("got = %v, want boom", got)
	}
}

func TestFakeRunnerFilesystemAndSBOM(t *testing.T) {
	f := scan.NewFakeRunner()
	f.QueueFilesystem(scan.Result{ArtifactName: "fs1"}, nil)
	f.QueueSBOM(nil)

	res, err := f.Filesystem(context.Background(), "/tmp")
	if err != nil || res.ArtifactName != "fs1" {
		t.Fatalf("Filesystem = %+v, %v", res, err)
	}
	if err := f.SBOM(context.Background(), "/tmp", scan.FormatCycloneDX, "/tmp/out"); err != nil {
		t.Fatalf("SBOM: %v", err)
	}
	// Drained
	if _, err := f.Filesystem(context.Background(), "/tmp"); !errors.Is(err, scan.ErrNothingQueued) {
		t.Errorf("drained Filesystem err = %v, want ErrNothingQueued", err)
	}
	if err := f.SBOM(context.Background(), "/tmp", scan.FormatSPDX, "/tmp/out"); !errors.Is(err, scan.ErrNothingQueued) {
		t.Errorf("drained SBOM err = %v, want ErrNothingQueued", err)
	}
}
