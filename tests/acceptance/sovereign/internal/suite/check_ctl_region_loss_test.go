package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// lossCluster is the kubectl of check CTL-G10 in the tests: two regions,
// region-a the writer and region-b the follower.
type lossCluster struct {
	host        map[string]string   // region id -> database HOST of bp-gitea; absent: the own cluster
	noGitDB     string              // the region without cluster gitea/gitea-pg
	syncJob     string              // the region that runs the one-minute copy job
	heads       map[string][]string // region id -> its answers to the heads script, one per read (the last repeats)
	pairs       string              // the CNPG clusters of the writer, items of a JSON list
	replicas    string              // the CNPG clusters of the follower, items of a JSON list
	replication string              // the rows of pg_stat_replication of the pair's primary
	sealed      string              // the region whose OpenBao is sealed
	calls       []string
	reads       map[string]int
}

const lossHead1, lossHead2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"

const lossPair = `{"metadata": {"name": "shared-pg", "namespace": "shared-data"},
  "spec": {"postgresql": {"synchronous": {"method": "first", "number": 1, "dataDurability": "required"}}},
  "status": {"currentPrimary": "shared-pg-1", "instanceNames": ["shared-pg-1", "shared-pg-2"]}}`

// lossPairReplica is the pair's replica cluster on the follower; its name is the
// application_name of its stream.
const lossPairReplica = `{"metadata": {"name": "shared-pg-replica", "namespace": "shared-data"},
  "spec": {"replica": {"enabled": true, "source": "shared-pg"}},
  "status": {"currentPrimary": "shared-pg-replica-1", "instanceNames": ["shared-pg-replica-1"]}}`

func (f *lossCluster) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	call := r.ID + ": " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case args[0] == "get" && args[1] == hrResource:
		host, ok := f.host[r.ID]
		if !ok {
			host = regionLossGitOwnHost
		}
		return []byte(`{"spec": {"values": {"gitea": {"gitea": {"config": {"database": {"HOST": "` + host + `"}}}}}}}`), nil
	case args[0] == "get" && args[1] == regionLossCNPG && args[2] == regionLossGitDB:
		if r.ID == f.noGitDB {
			return nil, errors.New(`Error from server (NotFound): clusters.postgresql.cnpg.io "gitea-pg" not found`)
		}
		return []byte(`{"status": {"readyInstances": 1}}`), nil
	case args[0] == "get" && args[1] == regionLossCNPG && args[2] == "-A":
		if r.ID == "region-b" {
			return []byte(`{"items": [` + f.replicas + `]}`), nil
		}
		return []byte(`{"items": [` + f.pairs + `]}`), nil
	case args[0] == "get" && args[1] == "cronjobs":
		if r.ID == f.syncJob {
			return []byte(`{"items": [{"metadata": {"name": "region-git-sync"}}]}`), nil
		}
		return []byte(`{"items": [{"metadata": {"name": "gitea-backup"}}]}`), nil
	case args[0] == "get" && args[1] == "pods":
		return []byte(`{"items": [{"metadata": {"name": "gitea-7c9-x"}, "status": {"phase": "Pending"}},
		  {"metadata": {"name": "gitea-7c9-y"}, "status": {"phase": "Running"}}]}`), nil
	case args[0] == "exec" && args[3] == "gitea-7c9-y" && args[len(args)-1] == regionLossHeads:
		answers := f.heads[r.ID]
		n := min(f.reads[r.ID], len(answers)-1)
		f.reads[r.ID]++
		return []byte(answers[n]), nil
	case args[0] == "exec" && slices.Contains(args, "psql"):
		b, _ := io.ReadAll(stdin)
		if !strings.HasPrefix(string(b), "BEGIN READ ONLY;\n"+regionLossReplSQL) {
			return nil, fmt.Errorf("not a read-only select: %q", b)
		}
		return []byte(f.replication), nil
	case args[0] == "-n" && args[1] == secBaoNS && slices.Contains(args, "status"):
		return []byte(fmt.Sprintf(`{"type": "transit", "initialized": true, "sealed": %v, "n": 5}`, r.ID == f.sealed)), nil
	}
	return nil, errors.New("unexpected call " + call)
}

// lossGood is a Sovereign on which every bound of R1 holds.
func lossGood() lossCluster {
	return lossCluster{
		heads: map[string][]string{
			"region-a": {"openova/sovereign-regions.git " + lossHead1 + "\nacme/empty.git -\n"},
			"region-b": {"openova/sovereign-regions.git " + lossHead1 + "\n"},
		},
		pairs:       lossPair,
		replicas:    lossPairReplica,
		replication: "shared-pg-2 potential\nshared-pg-replica sync\n",
	}
}

// failedIDs returns the identifiers of the failed steps of a run, in order.
func failedIDs(r *runner) []string {
	out := []string{}
	for _, s := range r.run.Steps {
		if s.Status == Fail {
			out = append(out, s.ID)
		}
	}
	return out
}

func TestRegionLossCheck(t *testing.T) {
	if _, ok := registry[regionLossID]; !ok || regionLossID != "CTL-G10" {
		t.Fatalf("check %s is not registered", regionLossID)
	}
	for name, c := range map[string]struct {
		change func(*lossCluster)
		step   int    // the step that must fail besides step 7; 0: none
		want   string // a part of the reason of that step
	}{
		"every bound holds": {func(*lossCluster) {}, 0, ""},
		"the follower's Git server uses the shared pair": {func(f *lossCluster) {
			f.host = map[string]string{"region-b": "shared-pg-mesh-rw.shared-data.svc.cluster.local:5432"}
		}, 2, `region region-b: the Git server's database is "shared-pg-mesh-rw.shared-data.svc.cluster.local:5432"`},
		"the follower has no gitea-pg": {func(f *lossCluster) { f.noGitDB = "region-b" }, 2, `region region-b: get clusters.postgresql.cnpg.io gitea-pg`},
		"the follower runs the one-minute job": {func(f *lossCluster) { f.syncJob = "region-b" }, 3,
			"region region-b: CronJob gitea/region-git-sync copies Git every minute"},
		"the follower is behind the writer": {func(f *lossCluster) {
			f.heads["region-b"] = []string{"openova/sovereign-regions.git " + lossHead2 + "\n"}
		}, 4, `follower region-b lacks the writer's head of 1 of 1 repositories 1ms after: openova/sovereign-regions.git (writer 111111111111, follower "222222222222")`},
		"the follower lacks a repository": {func(f *lossCluster) { f.heads["region-b"] = []string{"\n"} }, 4,
			`openova/sovereign-regions.git (writer 111111111111, follower "")`},
		"the writer moved on during the wait": {func(f *lossCluster) {
			line := "openova/sovereign-regions.git "
			f.heads["region-a"] = []string{line + lossHead1 + "\n", line + lossHead2 + "\n"}
			f.heads["region-b"] = []string{line + lossHead2 + "\n"}
		}, 0, ""},
		"the writer has no commit": {func(f *lossCluster) { f.heads["region-a"] = []string{"acme/empty.git -\n"} }, 4,
			"writer region-a: its Git server holds no repository with a commit; nothing is measured"},
		"the heads script prints something else": {func(f *lossCluster) { f.heads["region-b"] = []string{"fatal: not a git repository\n"} }, 4,
			`follower region-b: pod gitea-7c9-y printed "fatal: not a git repository"`},
		"the pair streams to its own instance only": {func(f *lossCluster) { f.replication = "shared-pg-2 sync\nshared-pg-replica async\n" }, 5,
			"shared-data/shared-pg streams to 0 synchronous standby(s) in a follower region, it declares 1"},
		"the synchronous standby is a cluster of the writer region": {func(f *lossCluster) {
			f.pairs = lossPair + `, {"metadata": {"name": "shared-pg-copy", "namespace": "shared-data"},
			  "spec": {"replica": {"enabled": true}}, "status": {"instanceNames": ["shared-pg-copy-1"]}}`
			f.replication = "shared-pg-2 potential\nshared-pg-copy sync\n"
		}, 5, "shared-data/shared-pg streams to 0 synchronous standby(s) in a follower region, it declares 1"},
		"the follower has no replica cluster": {func(f *lossCluster) { f.replicas = "" }, 5,
			"shared-data/shared-pg streams to 0 synchronous standby(s) in a follower region, it declares 1"},
		"the pair names no instance": {func(f *lossCluster) {
			f.pairs = strings.Replace(lossPair, `, "instanceNames": ["shared-pg-1", "shared-pg-2"]`, "", 1)
		}, 5, "shared-data/shared-pg names no instance in status.instanceNames; nothing is measured"},
		"the writer has no pair at required": {func(f *lossCluster) {
			f.pairs = strings.Replace(lossPair, `"required"`, `"preferred"`, 1)
		}, 5, "writer region-a has no database pair at dataDurability required; nothing is measured"},
		"a replica cluster is not the pair's primary": {func(f *lossCluster) {
			f.pairs = strings.Replace(lossPair, `"spec": {`, `"spec": {"replica": {"enabled": true}, `, 1)
		}, 5, "has no database pair at dataDurability required"},
		"the follower's OpenBao is sealed": {func(f *lossCluster) { f.sealed = "region-b" }, 6,
			"region region-b: OpenBao in openbao/openbao-0 is initialised true and sealed true"},
	} {
		f := lossGood()
		c.change(&f)
		f.reads = map[string]int{}
		r := runCheck(t, "none", regionLossCheck{kubectl: f.kubectl, lag: time.Millisecond}, &CheckContext{})
		want := []string{"CTL-G10.7"}
		if c.step > 0 {
			want = []string{fmt.Sprintf("CTL-G10.%d", c.step), "CTL-G10.7"}
		}
		got, errs := failedIDs(r), failedSteps(r)
		switch {
		case len(r.run.Steps) != 7:
			t.Errorf("%s: want 7 steps, got %d: %v", name, len(r.run.Steps), errs)
		case !slices.Equal(got, want):
			t.Errorf("%s: want the failed steps %v, got %v: %v", name, want, got, errs)
		case c.step > 0 && !strings.Contains(errs[0], c.want):
			t.Errorf("%s: want a reason with %q, got %q", name, c.want, errs[0])
		case errs[len(errs)-1] != regionLossNoDrill:
			t.Errorf("%s: step 7 does not say that no drill measures the recovery times: %q", name, errs[len(errs)-1])
		case r.run.Checks[0].Status != Fail:
			t.Errorf("%s: the check is %s; without the drill it must fail", name, r.run.Checks[0].Status)
		}
		for _, call := range f.calls {
			// A call "-n <namespace> <verb> ..." names its verb third.
			words := strings.Fields(call)
			verb := words[1]
			if verb == "-n" && len(words) > 3 {
				verb = words[3]
			}
			if verb != "get" && verb != "exec" {
				t.Errorf("%s: the check reads only, but it ran %s", name, call)
			}
		}
	}
}

// The Git heads are read on the writer before and after the wait, and on the
// follower after it.
func TestRegionLossCheckWaitsBeforeReadingTheFollower(t *testing.T) {
	f := lossGood()
	f.reads = map[string]int{}
	runCheck(t, "none", regionLossCheck{kubectl: f.kubectl, lag: time.Millisecond}, &CheckContext{})
	order := []string{}
	for _, call := range f.calls {
		if strings.HasSuffix(call, regionLossHeads) {
			order = append(order, strings.SplitN(call, ":", 2)[0])
		}
	}
	if want := []string{"region-a", "region-a", "region-b"}; !slices.Equal(order, want) {
		t.Errorf("the heads are read in the order %v, want %v", order, want)
	}
}

// A Sovereign of one region has no survivor: step 1 fails and nothing is read.
func TestRegionLossCheckNeedsAFollower(t *testing.T) {
	f := lossGood()
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	m := &Manifest{Checks: []ManifestCheck{entry(regionLossID, StateExpectedFail)}}
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a", Kubeconfig: "a"}}}
	r.runChecks(context.Background(), m, []Check{regionLossCheck{kubectl: f.kubectl}}, cc, true)
	if got := failedSteps(r); len(r.run.Steps) != 1 || len(got) != 1 ||
		got[0] != "the run has 1 region(s); a region loss needs a surviving region" || len(f.calls) != 0 {
		t.Errorf("want one failed step and no call, got %v and the calls %v", got, f.calls)
	}
}

// The wait for the follower ends with the run's context.
func TestRegionLossCheckStopsWaitingWithTheRun(t *testing.T) {
	f := lossGood()
	f.reads = map[string]int{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := regionLossCheck{kubectl: f.kubectl, lag: time.Hour}.gitLag(ctx, kube.Region{ID: "region-a"}, []kube.Region{{ID: "region-b"}}, Evidence{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want the context's error, got %v", err)
	}
}
