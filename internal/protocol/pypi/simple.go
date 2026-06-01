package pypi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
)

// FileLink is a row in the per-project Simple page: one wheel/sdist file.
// SHA256 is the hex digest (no "sha256:" prefix); URL is the resolved
// href that pip/uv should fetch.
type FileLink struct {
	Filename       string
	URL            string
	SHA256         string
	RequiresPython string
	Yanked         bool
	YankedReason   string
}

// PEP 691 content-type strings used by the Simple API. A request whose
// Accept header includes ContentTypeJSON gets JSON; HTML otherwise.
const (
	ContentTypeJSON = "application/vnd.pypi.simple.v1+json"
	ContentTypeHTML = "application/vnd.pypi.simple.v1+html"
)

// PEP 691 schema version. Bumping this is a breaking change for clients.
const apiVersion = "1.0"

// simpleIndexHTMLTmpl renders the top-level /simple/ page (PEP 503): one
// anchor per project. html/template auto-escapes project names so a
// hostile name cannot inject HTML.
const simpleIndexHTMLTmpl = `<!DOCTYPE html>
<html>
<head>
<meta name="pypi:repository-version" content="1.0">
<title>Simple index</title>
</head>
<body>
{{range .}}<a href="{{.}}/">{{.}}</a>
{{end}}</body>
</html>
`

// projectHTMLTmpl renders a per-project index page (PEP 503).
const projectHTMLTmpl = `<!DOCTYPE html>
<html>
<head>
<meta name="pypi:repository-version" content="1.0">
<title>Links for {{.Project}}</title>
</head>
<body>
<h1>Links for {{.Project}}</h1>
{{range .Files}}<a href="{{.URL}}#sha256={{.SHA256}}"{{if .RequiresPython}} data-requires-python="{{.RequiresPython}}"{{end}}{{if .Yanked}} data-yanked="{{.YankedReason}}"{{end}}>{{.Filename}}</a><br/>
{{end}}</body>
</html>
`

//nolint:gochecknoglobals // Templates compiled once at init.
var (
	simpleIndexTmpl  = template.Must(template.New("simple-index").Parse(simpleIndexHTMLTmpl))
	projectIndexTmpl = template.Must(template.New("project-index").Parse(projectHTMLTmpl))
)

// RenderSimpleHTML writes the PEP 503 top-level Simple index HTML for
// the supplied normalized project names.
func RenderSimpleHTML(w io.Writer, projectsNormalized []string) error {
	if projectsNormalized == nil {
		projectsNormalized = []string{}
	}
	if err := simpleIndexTmpl.Execute(w, projectsNormalized); err != nil {
		return fmt.Errorf("pypi: render simple index html: %w", err)
	}
	return nil
}

// RenderSimpleJSON writes the PEP 691 top-level Simple index as JSON:
//
//	{"meta":{"api-version":"1.0"},"projects":[{"name":"flask"}, ...]}
func RenderSimpleJSON(w io.Writer, projectsNormalized []string) error {
	type proj struct {
		Name string `json:"name"`
	}
	type doc struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Projects []proj `json:"projects"`
	}
	var d doc
	d.Meta.APIVersion = apiVersion
	d.Projects = make([]proj, 0, len(projectsNormalized))
	for _, p := range projectsNormalized {
		d.Projects = append(d.Projects, proj{Name: p})
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&d); err != nil {
		return fmt.Errorf("pypi: render simple index json: %w", err)
	}
	return nil
}

// RenderProjectHTML writes the per-project Simple page. project is the
// normalized name; files are pre-sorted by the caller.
func RenderProjectHTML(w io.Writer, project string, files []FileLink) error {
	data := struct {
		Project string
		Files   []FileLink
	}{Project: project, Files: files}
	if err := projectIndexTmpl.Execute(w, data); err != nil {
		return fmt.Errorf("pypi: render project html: %w", err)
	}
	return nil
}

// RenderProjectJSON writes the per-project Simple page as PEP 691 JSON:
//
//	{"meta":{"api-version":"1.0"},"name":"flask","files":[
//	   {"filename":"flask-2.3.0-py3-none-any.whl","url":"...",
//	    "hashes":{"sha256":"..."},"requires-python":">=3.8",
//	    "yanked":false}, ...]}
func RenderProjectJSON(w io.Writer, project string, files []FileLink) error {
	type fileDoc struct {
		Filename       string            `json:"filename"`
		URL            string            `json:"url"`
		Hashes         map[string]string `json:"hashes"`
		RequiresPython string            `json:"requires-python,omitempty"`
		Yanked         any               `json:"yanked,omitempty"`
	}
	type doc struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Name  string    `json:"name"`
		Files []fileDoc `json:"files"`
	}
	var d doc
	d.Meta.APIVersion = apiVersion
	d.Name = project
	d.Files = make([]fileDoc, 0, len(files))
	for _, f := range files {
		fd := fileDoc{
			Filename:       f.Filename,
			URL:            f.URL,
			Hashes:         map[string]string{"sha256": f.SHA256},
			RequiresPython: f.RequiresPython,
		}
		if f.Yanked {
			if f.YankedReason != "" {
				fd.Yanked = f.YankedReason
			} else {
				fd.Yanked = true
			}
		}
		d.Files = append(d.Files, fd)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&d); err != nil {
		return fmt.Errorf("pypi: render project json: %w", err)
	}
	return nil
}
