package pypi_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vladoportos/omnirepo/internal/protocol/pypi"
)

func TestRenderSimpleHTMLEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := pypi.RenderSimpleHTML(&buf, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "<title>Simple index</title>") {
		t.Fatalf("missing title: %s", out)
	}
	if !strings.Contains(out, `<meta name="pypi:repository-version"`) {
		t.Fatalf("missing repository-version: %s", out)
	}
}

func TestRenderSimpleHTMLProjects(t *testing.T) {
	var buf bytes.Buffer
	if err := pypi.RenderSimpleHTML(&buf, []string{"flask", "zope-interface"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `<a href="flask/">flask</a>`) {
		t.Fatalf("flask anchor missing: %s", out)
	}
	if !strings.Contains(out, `<a href="zope-interface/">zope-interface</a>`) {
		t.Fatalf("zope anchor missing: %s", out)
	}
}

func TestRenderSimpleJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := pypi.RenderSimpleJSON(&buf, []string{"flask", "zope-interface"}); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, buf.String())
	}
	if got.Meta.APIVersion != "1.0" {
		t.Fatalf("api-version=%q", got.Meta.APIVersion)
	}
	if len(got.Projects) != 2 || got.Projects[0].Name != "flask" {
		t.Fatalf("projects: %+v", got.Projects)
	}
}

func TestRenderProjectHTML(t *testing.T) {
	var buf bytes.Buffer
	files := []pypi.FileLink{
		{Filename: "flask-2.3.0-py3-none-any.whl", URL: "../../packages/flask-2.3.0-py3-none-any.whl",
			SHA256: "deadbeef", RequiresPython: ">=3.8"},
		{Filename: "flask-2.3.0.tar.gz", URL: "../../packages/flask-2.3.0.tar.gz", SHA256: "cafebabe"},
	}
	if err := pypi.RenderProjectHTML(&buf, "flask", files); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "<h1>Links for flask</h1>") {
		t.Fatalf("missing h1: %s", out)
	}
	if !strings.Contains(out, "#sha256=deadbeef") {
		t.Fatalf("missing sha256 fragment: %s", out)
	}
	if !strings.Contains(out, `data-requires-python="&gt;=3.8"`) {
		t.Fatalf("missing data-requires-python (escaped): %s", out)
	}
}

func TestRenderProjectHTMLEscapes(t *testing.T) {
	// Project name with HTML metacharacters must be escaped — html/template
	// auto-escapes.
	var buf bytes.Buffer
	files := []pypi.FileLink{{Filename: "<script>x</script>-1.0.tar.gz", URL: "u", SHA256: "h"}}
	if err := pypi.RenderProjectHTML(&buf, "<evil>", files); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>x</script>") {
		t.Fatalf("html/template did not escape script tag: %s", out)
	}
	if !strings.Contains(out, "&lt;evil&gt;") {
		t.Fatalf("project name not escaped: %s", out)
	}
}

func TestRenderProjectJSON(t *testing.T) {
	var buf bytes.Buffer
	files := []pypi.FileLink{
		{Filename: "flask-2.3.0-py3-none-any.whl", URL: "../packages/flask-2.3.0-py3-none-any.whl",
			SHA256: "deadbeef", RequiresPython: ">=3.8"},
	}
	if err := pypi.RenderProjectJSON(&buf, "flask", files); err != nil {
		t.Fatalf("render: %v", err)
	}
	var got struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Name  string `json:"name"`
		Files []struct {
			Filename       string            `json:"filename"`
			URL            string            `json:"url"`
			Hashes         map[string]string `json:"hashes"`
			RequiresPython string            `json:"requires-python"`
		} `json:"files"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, buf.String())
	}
	if got.Meta.APIVersion != "1.0" {
		t.Fatalf("api-version=%q", got.Meta.APIVersion)
	}
	if got.Name != "flask" {
		t.Fatalf("name=%q", got.Name)
	}
	if len(got.Files) != 1 || got.Files[0].Hashes["sha256"] != "deadbeef" {
		t.Fatalf("files: %+v", got.Files)
	}
	if got.Files[0].RequiresPython != ">=3.8" {
		t.Fatalf("requires-python=%q", got.Files[0].RequiresPython)
	}
}
