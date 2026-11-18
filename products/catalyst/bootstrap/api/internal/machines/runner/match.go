package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// The files, evidence kinds and schemas of the task match. The index is the one file the
// result document names; it names the findings file of each machine with its checksum.
const (
	FileMatchIndex             = "index.json"
	KindMatchIndex             = "match-index"
	KindFindings               = "vulnerability-findings"
	MatchIndexSchema           = "openova.io/machine-match-index/v1"
	FindingsSchema             = "openova.io/machine-vulnerability-findings/v1"
	MessageDatabaseUnavailable = "database-unavailable"
	MaxSBOMBytes               = 64 << 20
)

// MatchIndex is the file index.json of a match: the update time of the database it used
// (RFC 3339) and one entry per bill of materials, in the order of --sbom-keys.
type MatchIndex struct {
	Schema          string       `json:"schema"`
	DatabaseVersion string       `json:"databaseVersion"`
	Machines        []MatchEntry `json:"machines"`
}

// MatchEntry is one bill of materials of a match. FindingsKey and SHA256 name its
// findings file; Error is set instead when it could not be matched.
type MatchEntry struct {
	Machine     string `json:"machine"`
	SBOMKey     string `json:"sbomKey"`
	FindingsKey string `json:"findingsKey,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Findings is the findings file of one machine, _match/<run>/<namespace>/<machine>.json.
type Findings struct {
	Schema          string    `json:"schema"`
	Machine         string    `json:"machine"`
	SBOMKey         string    `json:"sbomKey"`
	DatabaseVersion string    `json:"databaseVersion"`
	Vulnerabilities []Finding `json:"vulnerabilities"`
}

// Finding is one advisory on one installed package. Severity is critical, high, medium,
// low or info; FixedVersion is empty when no release fixes it.
type Finding struct {
	ID               string `json:"id"`
	Package          string `json:"package"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion"`
	Severity         string `json:"severity"`
	PublishedAt      string `json:"publishedAt"`
}

// match fetches the vulnerability database once, from --db-repository or Trivy's own
// repositories, then matches each bill of materials of --sbom-keys against it offline
// and stores one findings file per machine. A database that cannot be fetched fails the
// task with database-unavailable and stores nothing. A bill of materials that cannot be
// read or matched gets an error in the index; the others are still matched.
func match(ctx context.Context, cfg Config, env Env) taskResult {
	cache := filepath.Join(env.WorkDir, "trivy-cache")
	download := []string{"image", "--download-db-only", "--cache-dir", cache}
	if cfg.DBRepository != "" {
		download = append(download, "--db-repository", cfg.DBRepository)
	}
	if r := collect(ctx, trivyEnv, []int{0}, env.Trivy, download...); r != nil {
		if r.outcome == runresult.Failed {
			r.message = MessageDatabaseUnavailable + ": " + r.message
		}
		return *r
	}
	version, r := databaseVersion(ctx, env, cache)
	if r != nil {
		return *r
	}
	client, err := bucket(cfg, env)
	if err != nil {
		return failed("%s%v", messageUploadFailed, err)
	}
	index := MatchIndex{Schema: MatchIndexSchema, DatabaseVersion: version, Machines: []MatchEntry{}}
	for i, key := range cfg.SBOMKeys {
		if ctx.Err() != nil {
			return taskResult{outcome: runresult.Cancelled, message: "cancelled while matching"}
		}
		index.Machines = append(index.Machines, matchOne(ctx, client, cfg, env, cache, i, key, version))
	}
	data, err := json.Marshal(index)
	if err == nil {
		err = os.WriteFile(filepath.Join(env.WorkDir, FileMatchIndex), data, 0o600)
	}
	if err != nil {
		return failed("the index was not written: %v", err)
	}
	if err := writeMarker(env, TaskMatch); err != nil {
		return failed("the completion marker was not written: %v", err)
	}
	return taskResult{outcome: runresult.Succeeded, files: []file{{KindMatchIndex, FileMatchIndex}}}
}

// databaseVersion reads the update time of the fetched database from trivy version.
func databaseVersion(ctx context.Context, env Env, cache string) (string, *taskResult) {
	var out bytes.Buffer
	if r := collectTo(ctx, &out, trivyEnv, []int{0}, env.Trivy, "version", "--format", "json", "--cache-dir", cache); r != nil {
		if r.outcome == runresult.Failed {
			r.message = MessageDatabaseUnavailable + ": " + r.message
		}
		return "", r
	}
	var v struct {
		VulnerabilityDB *struct {
			UpdatedAt time.Time `json:"UpdatedAt"`
		} `json:"VulnerabilityDB"`
	}
	if json.Unmarshal(out.Bytes(), &v) != nil || v.VulnerabilityDB == nil || v.VulnerabilityDB.UpdatedAt.IsZero() {
		r := failed("%s: trivy version names no update time of the database", MessageDatabaseUnavailable)
		return "", &r
	}
	return v.VulnerabilityDB.UpdatedAt.UTC().Format(time.RFC3339), nil
}

// matchOne matches the bill of materials of one machine and stores its findings file.
func matchOne(ctx context.Context, client *minio.Client, cfg Config, env Env, cache string, i int, key, version string) MatchEntry {
	machine, _ := SBOMMachine(key)
	e := MatchEntry{Machine: machine, SBOMKey: key}
	sbom := filepath.Join(env.WorkDir, fmt.Sprintf("sbom-%d.cdx.json", i))
	report := filepath.Join(env.WorkDir, fmt.Sprintf("trivy-%d.json", i))
	defer func() { _ = os.Remove(sbom); _ = os.Remove(report) }()
	if err := fetch(ctx, client, cfg.Evidence.Bucket, key, sbom); err != nil {
		e.Error = "sbom-unavailable: " + err.Error()
		return e
	}
	if r := collect(ctx, trivyEnv, []int{0}, env.Trivy, "sbom", "--format", "json", "--skip-db-update",
		"--skip-java-db-update", "--offline-scan", "--cache-dir", cache, "--output", report, sbom); r != nil {
		e.Error = r.message
		return e
	}
	vulns, err := readTrivyReport(report)
	if err != nil {
		e.Error = err.Error()
		return e
	}
	data, err := json.Marshal(Findings{Schema: FindingsSchema, Machine: machine, SBOMKey: key, DatabaseVersion: version, Vulnerabilities: vulns})
	name := machine + ".json"
	path := filepath.Join(env.WorkDir, filepath.FromSlash(name))
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		e.Error = "the findings file was not written: " + err.Error()
		return e
	}
	stored, err := put(ctx, client, cfg, path, file{KindFindings, name})
	if err != nil {
		e.Error = messageUploadFailed + err.Error()
		return e
	}
	e.FindingsKey, e.SHA256 = stored.Key, stored.SHA256
	return e
}

// fetch copies one object of the evidence bucket, at most MaxSBOMBytes long, to path.
func fetch(ctx context.Context, client *minio.Client, bucketName, key, path string) error {
	obj, err := client.GetObject(ctx, bucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = obj.Close() }()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(obj, MaxSBOMBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxSBOMBytes {
		err = errors.New("longer than 64 MiB")
	}
	return err
}

// readTrivyReport reads the findings of a Trivy JSON report: one per advisory, package
// and installed version, sorted by advisory, package and version.
func readTrivyReport(path string) ([]Finding, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a fixed name in the work folder
	var report struct {
		Results []struct {
			Vulnerabilities []struct {
				VulnerabilityID, PkgName, InstalledVersion, FixedVersion, Severity, PublishedDate string
			}
		}
	}
	if err != nil || json.Unmarshal(data, &report) != nil {
		return nil, errors.New("trivy wrote no readable report")
	}
	seen, out := map[string]bool{}, []Finding{}
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			k := v.VulnerabilityID + "\x00" + v.PkgName + "\x00" + v.InstalledVersion
			if v.VulnerabilityID == "" || v.PkgName == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, Finding{ID: v.VulnerabilityID, Package: v.PkgName, InstalledVersion: v.InstalledVersion,
				FixedVersion: v.FixedVersion, Severity: severity(v.Severity), PublishedAt: v.PublishedDate})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.InstalledVersion < b.InstalledVersion
	})
	return out, nil
}

// severity maps a Trivy severity to the severity of a report: UNKNOWN and anything else
// that is not critical, high, medium or low is info.
func severity(s string) string {
	switch s = strings.ToLower(s); s {
	case "critical", "high", "medium", "low":
		return s
	}
	return "info"
}
