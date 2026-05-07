package scan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrEmptyInput is returned when ParseTrivyJSON is called with a zero-length
// byte slice (T-02-03-06: treat empty stdout as a failure, not "no vulns").
var ErrEmptyInput = errors.New("scan: empty trivy output")

// trivyReport mirrors the subset of Trivy JSON output we care about. Extra
// fields at any level are tolerated (D-24: DisallowUnknownFields = false).
type trivyReport struct {
	SchemaVersion int                `json:"SchemaVersion"`
	ArtifactName  string             `json:"ArtifactName"`
	ArtifactType  string             `json:"ArtifactType"`
	Metadata      trivyReportMeta    `json:"Metadata"`
	Results       []trivyReportBlock `json:"Results"`
}

type trivyReportMeta struct {
	// OS.Family/Name are the usual carrier of DB version hints in image scans.
	OS trivyReportOS `json:"OS"`
	// Trivy ≥ 0.59 adds a top-level DBVersion field on some artifact types.
	DBVersion string `json:"DBVersion"`
}

type trivyReportOS struct {
	Family string `json:"Family"`
	Name   string `json:"Name"`
}

type trivyReportBlock struct {
	Target            string               `json:"Target"`
	Class             string               `json:"Class"`
	Type              string               `json:"Type"`
	Vulnerabilities   []trivyReportVuln    `json:"Vulnerabilities"`
	Misconfigurations []trivyReportMisconf `json:"Misconfigurations"`
}

type trivyReportVuln struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Title            string `json:"Title"`
	Description      string `json:"Description"`
}

// trivyReportMisconf is one Kubernetes / IaC misconfiguration finding.
// Relevant primarily to Helm chart scans, but also surfaces for Dockerfile
// and Terraform when those are present in the scan root (F-08.2).
type trivyReportMisconf struct {
	ID          string `json:"ID"`
	AVDID       string `json:"AVDID"`
	Title       string `json:"Title"`
	Description string `json:"Description"`
	Severity    string `json:"Severity"`
	Resolution  string `json:"Resolution"`
	PrimaryURL  string `json:"PrimaryURL"`
	Status      string `json:"Status"`
}

// ParseTrivyJSON decodes a Trivy JSON document into a Result. It is
// deliberately tolerant of unknown fields so Trivy schema additions don't
// break OmniRepo (SCAN-06, D-24). Snapshot tests under testdata/trivy/ lock
// the known-good shapes against drift.
//
// Unknown severity strings (future values Trivy may emit) bucket into
// Summary["unknown"] rather than silently disappearing.
func ParseTrivyJSON(b []byte) (Result, error) {
	if len(b) == 0 {
		return Result{}, ErrEmptyInput
	}
	var r trivyReport
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("trivy parse: %w", err)
	}
	summary := map[string]int{
		"critical": 0,
		"high":     0,
		"medium":   0,
		"low":      0,
		"unknown":  0,
	}
	var vulns []Vuln
	for _, block := range r.Results {
		for _, v := range block.Vulnerabilities {
			sev := strings.ToLower(v.Severity)
			if _, ok := summary[sev]; !ok {
				sev = "unknown"
			}
			summary[sev]++
			vulns = append(vulns, Vuln{
				CVEID:            v.VulnerabilityID,
				Package:          v.PkgName,
				InstalledVersion: v.InstalledVersion,
				FixedVersion:     v.FixedVersion,
				Severity:         v.Severity,
				Title:            v.Title,
				Description:      v.Description,
			})
		}
		// F-08.2: Helm chart scans (and any other misconfig-heavy protocol)
		// surface findings in Misconfigurations, not Vulnerabilities. Without
		// this loop every Helm chart scan summarized as all-zeros and the
		// repos.block_on_severity gate was effectively defeated.
		//
		// An active Trivy finding may have Status="FAIL" or omit the field
		// entirely (observed on some v0.69 outputs); PASS/EXCEPTION etc. are
		// already-resolved and must not inflate the gate.
		for i, m := range block.Misconfigurations {
			if m.Status != "" && m.Status != "FAIL" {
				continue
			}
			sev := strings.ToLower(m.Severity)
			if _, ok := summary[sev]; !ok {
				sev = "unknown"
			}
			summary[sev]++
			// Codex-01: Trivy always emits ID today, but guard against a
			// future schema where both ID and AVDID are absent — an empty
			// cve_id would insert silently (NOT NULL allows "") and then
			// never index into FTS. Synthesize a stable placeholder so
			// duplicate MISCONF rows dedup on (block.Target, index).
			id := m.ID
			if id == "" {
				id = m.AVDID
			}
			if id == "" {
				id = fmt.Sprintf("MISCONF-%s-%d", block.Target, i)
			}
			desc := m.Description
			if m.Resolution != "" {
				if desc != "" {
					desc += " "
				}
				desc += "Resolution: " + m.Resolution
			}
			vulns = append(vulns, Vuln{
				CVEID:       id,
				Package:     block.Target,
				Severity:    m.Severity,
				Title:       m.Title,
				Description: desc,
			})
		}
	}
	dbVersion := r.Metadata.DBVersion
	if dbVersion == "" && r.Metadata.OS.Family != "" {
		// Fall back to OS label when the dedicated field is absent. Callers
		// use this only as an audit hint; an empty string is fine.
		dbVersion = r.Metadata.OS.Family + "/" + r.Metadata.OS.Name
	}
	return Result{
		Summary:         summary,
		Vulnerabilities: vulns,
		SchemaVersion:   r.SchemaVersion,
		TrivyDBVersion:  dbVersion,
		ArtifactName:    r.ArtifactName,
	}, nil
}
