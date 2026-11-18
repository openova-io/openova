package runner

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// The tasks and modes the arguments may name. This version runs the tasks inventory and
// hardening in node mode, and the task match, which has no target and no mode; any other
// valid task or mode gives outcome Refused.
var (
	tasks = []string{TaskFacts, TaskInventory, TaskHardening, TaskPatch, TaskTest, TaskMatch}
	modes = []string{ModeNode, ModeSSH}
)

// Task and mode names.
const (
	TaskFacts     = "facts"
	TaskInventory = "inventory"
	TaskHardening = "hardening"
	TaskPatch     = "patch"
	TaskTest      = "test"
	TaskMatch     = "match"
	ModeNode      = "node"
	ModeSSH       = "ssh"
)

// Ref names an object as <namespace>/<name>.
type Ref struct {
	Namespace string
	Name      string
}

// String gives <namespace>/<name>.
func (r Ref) String() string { return r.Namespace + "/" + r.Name }

// Evidence says where the run stores its files: an S3 endpoint, its region, the bucket
// and the key prefix <machine namespace>/<machine name>/<run name>/, or _match/<run name>/
// for the task match.
type Evidence struct {
	Endpoint string
	Region   string
	Bucket   string
	Prefix   string
}

// Config is one run, read from the arguments.
type Config struct {
	Task           string
	Mode           string
	Machine        Ref
	Run            Ref
	Address        string
	OSFamily       string
	OSVersion      string
	ProfileID      string
	ContentRelease string
	DisabledRules  []string
	SBOMKeys       []string // match: the bills of materials to match
	DBRepository   string   // match: the OCI repository of the vulnerability database; empty: Trivy's own
	Evidence       Evidence
}

// MatchPrefix is the key prefix of the files of the task match of one run.
func MatchPrefix(run string) string { return "_match/" + run + "/" }

// MaxSBOMKeys is the largest number of bills of materials one match task reads.
const MaxSBOMKeys = 1000

var (
	namespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	nameRE      = regexp.MustCompile(`^[a-z0-9]([-.a-z0-9]{0,251}[a-z0-9])?$`)
	familyRE    = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)
	versionRE   = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){0,2}$`)
	profileRE   = regexp.MustCompile(`^xccdf_[A-Za-z0-9.-]+_profile_[A-Za-z0-9_.-]+$`)
	ruleRE      = regexp.MustCompile(`^xccdf_[A-Za-z0-9.-]+_rule_[A-Za-z0-9_.-]+$`)
	releaseRE   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	repoRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]([-._a-z0-9]*[a-z0-9])?)+(:[A-Za-z0-9_][-.A-Za-z0-9_]{0,127})?$`)
	bucketRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRE    = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// ParseArgs reads the arguments of one run. It fails on an unknown flag, on a missing
// required flag and on a value that does not have the form of its flag. The flags
// --profile-id and --content-release are required, and --disabled-rules is allowed,
// only for the task hardening. The task match takes --run, --sbom-keys, the evidence
// flags and, optionally, --db-repository, and none of the flags of a target.
func ParseArgs(args []string) (Config, error) {
	fs := flag.NewFlagSet("machine-runner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c Config
	var machine, run, disabled, sboms string
	fs.StringVar(&c.Task, "task", "", "the task: facts, inventory, hardening, patch or test")
	fs.StringVar(&c.Mode, "mode", "", "the mode: node or ssh")
	fs.StringVar(&machine, "machine", "", "the Machine, as <namespace>/<name>")
	fs.StringVar(&run, "run", "", "the MachineRun, as <namespace>/<name>")
	fs.StringVar(&c.Address, "address", "", "the address of the target")
	fs.StringVar(&c.OSFamily, "os-family", "", "the operating-system family of the target")
	fs.StringVar(&c.OSVersion, "os-version", "", "the operating-system version of the target")
	fs.StringVar(&c.ProfileID, "profile-id", "", "hardening: the XCCDF profile identifier")
	fs.StringVar(&c.ContentRelease, "content-release", "", "hardening: the ComplianceAsCode release")
	fs.StringVar(&disabled, "disabled-rules", "", "hardening: comma-separated XCCDF rule identifiers that are not evaluated")
	fs.StringVar(&sboms, "sbom-keys", "", "match: comma-separated object keys <namespace>/<machine>/<run>/sbom.cdx.json")
	fs.StringVar(&c.DBRepository, "db-repository", "", "match: the OCI repository of the vulnerability database")
	fs.StringVar(&c.Evidence.Endpoint, "evidence-endpoint", "", "the S3 endpoint, https://host[:port]")
	fs.StringVar(&c.Evidence.Region, "evidence-region", "", "the region of the S3 endpoint")
	fs.StringVar(&c.Evidence.Bucket, "evidence-bucket", "", "the evidence bucket")
	fs.StringVar(&c.Evidence.Prefix, "evidence-prefix", "", "the key prefix <namespace>/<machine>/<run>/")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if !contains(tasks, c.Task) {
		bad("--task must be one of %s", strings.Join(tasks, ", "))
	}
	var ok bool
	if c.Task == TaskMatch {
		if c.Mode != "" || machine != "" || c.Address != "" || c.OSFamily != "" || c.OSVersion != "" {
			bad("--mode, --machine, --address, --os-family and --os-version do not belong to the task match")
		}
	} else {
		if !contains(modes, c.Mode) {
			bad("--mode must be one of %s", strings.Join(modes, ", "))
		}
		if c.Machine, ok = parseRef(machine); !ok {
			bad("--machine must be <namespace>/<name>")
		}
	}
	if c.Run, ok = parseRef(run); !ok {
		bad("--run must be <namespace>/<name>")
	}
	if c.Task == TaskMatch {
		if c.SBOMKeys = splitKeys(sboms); len(c.SBOMKeys) == 0 || len(c.SBOMKeys) > MaxSBOMKeys {
			bad("--sbom-keys must name 1 to %d bills of materials", MaxSBOMKeys)
		}
		for _, k := range c.SBOMKeys {
			if _, ok := SBOMMachine(k); !ok {
				bad("--sbom-keys: %q is not <namespace>/<machine>/<run>/%s", k, FileSBOM)
			}
		}
		if c.DBRepository != "" && !ValidRepository(c.DBRepository) {
			bad("--db-repository must be an OCI repository such as registry.example/trivy-db:2")
		}
	} else {
		if c.Address != "" && net.ParseIP(c.Address) == nil {
			bad("--address must be an IP address")
		}
		if !familyRE.MatchString(c.OSFamily) {
			bad("--os-family must be a lower-case word")
		}
		if !versionRE.MatchString(c.OSVersion) {
			bad("--os-version must be a version such as 24.04")
		}
		if sboms != "" || c.DBRepository != "" {
			bad("--sbom-keys and --db-repository belong to the task match")
		}
	}
	if c.Task == TaskHardening {
		if !profileRE.MatchString(c.ProfileID) {
			bad("--profile-id must be an XCCDF profile identifier")
		}
		if !releaseRE.MatchString(c.ContentRelease) {
			bad("--content-release must be a release such as 0.1.82")
		}
		if disabled != "" {
			for _, rule := range strings.Split(disabled, ",") {
				if !ruleRE.MatchString(rule) {
					bad("--disabled-rules: %q is not an XCCDF rule identifier", rule)
				}
				c.DisabledRules = append(c.DisabledRules, rule)
			}
		}
	} else if c.ProfileID != "" || c.ContentRelease != "" || disabled != "" {
		bad("--profile-id, --content-release and --disabled-rules belong to the task hardening")
	}
	if err := checkEndpoint(c.Evidence.Endpoint); err != nil {
		bad("--evidence-endpoint %s", err)
	}
	if !regionRE.MatchString(c.Evidence.Region) {
		bad("--evidence-region must be a region name")
	}
	if !bucketRE.MatchString(c.Evidence.Bucket) {
		bad("--evidence-bucket must be a bucket name")
	}
	want := c.Machine.Namespace + "/" + c.Machine.Name + "/" + c.Run.Name + "/"
	if c.Task == TaskMatch {
		want = MatchPrefix(c.Run.Name)
	}
	if c.Evidence.Prefix != want {
		bad("--evidence-prefix must be %q", want)
	}
	if len(problems) > 0 {
		return Config{}, errors.New(strings.Join(problems, "; "))
	}
	return c, nil
}

// ValidRepository reports whether s has the form of the OCI repository of
// --db-repository, such as registry.example/trivy-db:2.
func ValidRepository(s string) bool { return repoRE.MatchString(s) }

// splitKeys reads a comma-separated list; an empty text gives none.
func splitKeys(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// SBOMMachine returns the Machine, <namespace>/<name>, of the object key of a bill of
// materials, <namespace>/<machine>/<run>/sbom.cdx.json.
func SBOMMachine(key string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[3] != FileSBOM || !nameRE.MatchString(parts[2]) {
		return "", false
	}
	ref, ok := parseRef(parts[0] + "/" + parts[1])
	return ref.String(), ok
}

// parseRef reads <namespace>/<name>.
func parseRef(s string) (Ref, bool) {
	ns, name, found := strings.Cut(s, "/")
	if !found || !namespaceRE.MatchString(ns) || !nameRE.MatchString(name) {
		return Ref{}, false
	}
	return Ref{Namespace: ns, Name: name}, true
}

// checkEndpoint accepts https://host[:port] with no path, query or user part.
func checkEndpoint(s string) error {
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Host == "":
		return errors.New("must be https://host[:port]")
	case u.Scheme != "https":
		return errors.New("must use https")
	case u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return errors.New("must be https://host[:port] with nothing after the port")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
