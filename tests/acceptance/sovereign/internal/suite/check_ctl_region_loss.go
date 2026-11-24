package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CTL-G10, region loss: recovery within the numbers of decision record
// 0027 (docs/adr/0027-region-loss-contract.md, decision R1). The check reads
// only. The writer region is the first region of the run, the primary
// (ADR-0021); every other region is a follower. Each step is one data class of
// R1 and judges, on the live Sovereign, the quantity that bounds its number:
//  1. the Sovereign has a follower region, the one that survives a loss;
//  2. Git (R2): in every region the Git server's database is its own cluster
//     gitea-pg of namespace gitea, which is ready, and not the database pair
//     shared-pg that a lost writer region takes with it;
//  3. Git (R2): no region runs the one-minute copy job gitea/region-git-sync;
//  4. Git, at most 60 seconds of commits lost: the check reads the head of
//     the default branch of every repository on the writer's Git server,
//     waits 60 seconds and reads the heads on each follower: each follower
//     holds the head the writer had, or the writer's head of after the wait;
//  5. database pair at dataDurability required, nothing lost: every CNPG
//     cluster of the writer region that declares it streams to at least as
//     many synchronous standbys as it declares, each a replica cluster of a
//     follower region. A writer region with no such cluster, or a cluster
//     without instance names, has nothing measured, and fails;
//  6. OpenBao, recovery takes no time: in every region openbao-0 is
//     initialised and unsealed, so each region serves its own store;
//  7. the recovery times (the survivor's Git accepts commits within 10
//     minutes, the survivor of a database pair is writable within 15 minutes,
//     the survivor's edge answers the name of an Application within 2
//     minutes) are measured from a loss of the writer region and the
//     promotion commit of R4. No loss drill exists yet, so this step fails
//     and says why: the check stays expected-fail until the package that
//     implements promotion by commit adds the drill and sets it to required.
//
// etcd and the domain events have no number of recovery to judge (R1). Every
// step runs whatever an earlier one gave, except that a Sovereign of one
// region stops after step 1.
const (
	regionLossID         = "CTL-G10"
	regionLossGitNS      = "gitea"
	regionLossGitHR      = "bp-gitea" // flux-system/bp-gitea, bootstrap-kit slot 10
	regionLossGitOwnHost = "gitea-pg-rw.gitea.svc.cluster.local:5432"
	regionLossGitDB      = "gitea-pg"
	regionLossSyncJob    = "region-git-sync"
	regionLossGitPods    = "app.kubernetes.io/name=gitea"
	regionLossGitBox     = "gitea"
	regionLossGitLag     = 60 * time.Second
	regionLossCNPG       = "clusters.postgresql.cnpg.io"
	regionLossReplSQL    = "SELECT application_name || ' ' || sync_state FROM pg_stat_replication"
)

// regionLossHeads prints one line "<owner>/<name>.git <head>" for every
// repository of the Git server; <head> is "-" for a repository without a
// commit. HEAD of a repository of the server names its default branch.
const regionLossHeads = `cd /data/git/gitea-repositories || exit 3
for d in */*.git; do
  [ -d "$d" ] || continue
  h=$(git -c safe.directory='*' --git-dir="$d" rev-parse -q --verify 'HEAD^{commit}') || h=-
  echo "$d $h"
done`

// regionLossNoDrill is the reason of step 7.
const regionLossNoDrill = "no loss drill measures the recovery times yet: the survivor's Git accepting commits within 10 minutes, " +
	"its database pair writable within 15 minutes and its edge answering an Application's name within 2 minutes need a lost " +
	"writer region and the promotion commit of decision record 0027 R4, which the package that implements promotion by commit brings"

type regionLossCheck struct {
	kubectl kubectlFunc
	lag     time.Duration // 0: regionLossGitLag
}

func init() { RegisterCheck(regionLossCheck{kubectl: kube.Region.Run}) }

func (regionLossCheck) ID() string { return regionLossID }

func (c regionLossCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if !checkStep(rec, regionLossID, 1, "the Sovereign has a follower region that survives the loss of the writer", func(ev Evidence) error {
		ids := []string{}
		for _, r := range cc.Regions {
			ids = append(ids, r.ID)
		}
		ev["regions"] = ids
		if len(ids) < 2 {
			return fmt.Errorf("the run has %d region(s); a region loss needs a surviving region", len(ids))
		}
		ev["writer"] = ids[0]
		return nil
	}) {
		return nil
	}
	writer, followers := cc.Regions[0], cc.Regions[1:]
	checkStep(rec, regionLossID, 2, "every region's Git server uses its own database cluster gitea-pg", func(ev Evidence) error {
		return regionLossEach(cc.Regions, ev, func(r kube.Region, ev Evidence) error { return c.ownGitDB(ctx, r, ev) })
	})
	checkStep(rec, regionLossID, 3, "no region runs the one-minute Git copy job", func(ev Evidence) error {
		return regionLossEach(cc.Regions, ev, func(r kube.Region, ev Evidence) error { return c.noSyncJob(ctx, r, ev) })
	})
	checkStep(rec, regionLossID, 4, "every follower holds the writer's Git heads within 60 seconds", func(ev Evidence) error {
		return c.gitLag(ctx, writer, followers, ev)
	})
	checkStep(rec, regionLossID, 5, "every database pair at dataDurability required streams synchronously out of its region", func(ev Evidence) error {
		return c.syncPairs(ctx, writer, followers, ev)
	})
	checkStep(rec, regionLossID, 6, "every region's OpenBao serves its own store", func(ev Evidence) error {
		return regionLossEach(cc.Regions, ev, func(r kube.Region, ev Evidence) error { return c.ownBao(ctx, r, ev) })
	})
	checkStep(rec, regionLossID, 7, "the recovery times after a loss of the writer region are measured", func(Evidence) error {
		return errors.New(regionLossNoDrill)
	})
	return nil
}

// regionLossEach runs fn for every region with the evidence of that region, and
// joins the faults of all regions.
func regionLossEach(regions []kube.Region, ev Evidence, fn func(kube.Region, Evidence) error) error {
	faults := []string{}
	for _, r := range regions {
		rev := Evidence{}
		ev[r.ID] = rev
		if err := fn(r, rev); err != nil {
			faults = append(faults, fmt.Sprintf("region %s: %v", r.ID, err))
		}
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// object runs kubectl get ... -o json and reads one object.
func (c regionLossCheck) object(ctx context.Context, r kube.Region, args ...string) (kube.Object, error) {
	out, err := c.kubectl(r, ctx, nil, append(append([]string{"get"}, args...), "-o", "json")...)
	if err != nil {
		return nil, fmt.Errorf("get %s: %v", strings.Join(args, " "), err)
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("get %s printed no JSON object: %v", strings.Join(args, " "), err)
	}
	return o, nil
}

func (c regionLossCheck) ownGitDB(ctx context.Context, r kube.Region, ev Evidence) error {
	hr, err := c.object(ctx, r, hrResource, regionLossGitHR, "-n", "flux-system")
	if err != nil {
		return err
	}
	host := hr.Str("spec", "values", "gitea", "gitea", "config", "database", "HOST")
	ev["database_host"] = host
	if host != regionLossGitOwnHost {
		return fmt.Errorf("the Git server's database is %q, not its own cluster %s", host, regionLossGitOwnHost)
	}
	db, err := c.object(ctx, r, regionLossCNPG, regionLossGitDB, "-n", regionLossGitNS)
	if err != nil {
		return err
	}
	ready := db.Int("status", "readyInstances")
	ev["ready_instances"] = ready
	if ready < 1 {
		return fmt.Errorf("cluster %s/%s has no ready instance", regionLossGitNS, regionLossGitDB)
	}
	return nil
}

func (c regionLossCheck) noSyncJob(ctx context.Context, r kube.Region, ev Evidence) error {
	list, err := c.object(ctx, r, "cronjobs", "-n", regionLossGitNS)
	if err != nil {
		return err
	}
	names := []string{}
	for _, j := range list.Objects("items") {
		names = append(names, j.Name())
	}
	ev["cronjobs"] = names
	if slices.Contains(names, regionLossSyncJob) {
		return fmt.Errorf("CronJob %s/%s copies Git every minute; the writer's push mirror replaces it", regionLossGitNS, regionLossSyncJob)
	}
	return nil
}

// heads reads the default-branch head of every repository of the region's Git
// server. A repository without a commit is left out.
func (c regionLossCheck) heads(ctx context.Context, r kube.Region) (map[string]string, error) {
	pods, err := c.object(ctx, r, "pods", "-n", regionLossGitNS, "-l", regionLossGitPods)
	if err != nil {
		return nil, err
	}
	running := []string{}
	for _, p := range pods.Objects("items") {
		if p.Str("status", "phase") == "Running" {
			running = append(running, p.Name())
		}
	}
	if len(running) == 0 {
		return nil, fmt.Errorf("no Git server pod (%s) runs in namespace %s", regionLossGitPods, regionLossGitNS)
	}
	sort.Strings(running)
	out, err := c.kubectl(r, ctx, nil, "exec", "-n", regionLossGitNS, running[0], "-c", regionLossGitBox, "--", "sh", "-c", regionLossHeads)
	if err != nil {
		return nil, fmt.Errorf("reading the heads in pod %s: %v", running[0], err)
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			if line != "" {
				return nil, fmt.Errorf("pod %s printed %q, not \"<repository> <head>\"", running[0], line)
			}
			continue
		}
		if f[1] != "-" {
			heads[f[0]] = f[1]
		}
	}
	return heads, nil
}

func (c regionLossCheck) gitLag(ctx context.Context, writer kube.Region, followers []kube.Region, ev Evidence) error {
	lag := c.lag
	if lag == 0 {
		lag = regionLossGitLag
	}
	before, err := c.heads(ctx, writer)
	if err != nil {
		return fmt.Errorf("writer %s: %v", writer.ID, err)
	}
	ev["repositories"], ev["lag_seconds"] = len(before), lag.Seconds()
	if len(before) == 0 {
		return fmt.Errorf("writer %s: its Git server holds no repository with a commit; nothing is measured", writer.ID)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(lag):
	}
	after, err := c.heads(ctx, writer)
	if err != nil {
		return fmt.Errorf("writer %s after the wait: %v", writer.ID, err)
	}
	repos := make([]string, 0, len(before))
	for repo := range before {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	faults := []string{}
	for _, f := range followers {
		got, err := c.heads(ctx, f)
		if err != nil {
			faults = append(faults, fmt.Sprintf("follower %s: %v", f.ID, err))
			continue
		}
		behind := []string{}
		for _, repo := range repos {
			if h := got[repo]; h != before[repo] && h != after[repo] {
				behind = append(behind, fmt.Sprintf("%s (writer %s, follower %q)", repo, regionLossShort(before[repo]), regionLossShort(h)))
			}
		}
		ev[f.ID] = Evidence{"repositories": len(got), "behind": len(behind)}
		if len(behind) > 0 {
			faults = append(faults, fmt.Sprintf("follower %s lacks the writer's head of %d of %d repositories %v after: %s",
				f.ID, len(behind), len(repos), lag, strings.Join(behind, ", ")))
		}
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// regionLossShort is the first 12 characters of a commit id.
func regionLossShort(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// syncPairs counts, for every pair of the writer, the synchronous standbys
// that are a replica cluster of a follower region: a row of pg_stat_replication
// counts when its application_name is the name (CNPG's streaming default) or
// an instance of a cluster with spec.replica.enabled on a follower, and no
// cluster of the writer region has that name or instance.
func (c regionLossCheck) syncPairs(ctx context.Context, writer kube.Region, followers []kube.Region, ev Evidence) error {
	list, err := c.object(ctx, writer, regionLossCNPG, "-A")
	if err != nil {
		return fmt.Errorf("writer %s: %v", writer.ID, err)
	}
	pairs, faults := Evidence{}, []string{}
	ev["pairs"] = pairs
	local, remote := regionLossNames(list, false), map[string]bool{}
	for _, f := range followers {
		fl, err := c.object(ctx, f, regionLossCNPG, "-A")
		if err != nil {
			faults = append(faults, fmt.Sprintf("follower %s: %v", f.ID, err))
			continue
		}
		for n := range regionLossNames(fl, true) {
			remote[n] = true
		}
	}
	for _, cl := range list.Objects("items") {
		if cl.Str("spec", "postgresql", "synchronous", "dataDurability") != "required" || cl.Get("spec", "replica", "enabled") == true {
			continue
		}
		key := cl.Namespace() + "/" + cl.Name()
		want := max(cl.Int("spec", "postgresql", "synchronous", "number"), 1)
		if names, _ := cl.Get("status", "instanceNames").([]any); len(names) == 0 {
			pairs[key] = Evidence{"error": "no status.instanceNames"}
			faults = append(faults, fmt.Sprintf("%s names no instance in status.instanceNames; nothing is measured", key))
			continue
		}
		rows, err := c.replication(ctx, writer, cl.Namespace(), cl.Str("status", "currentPrimary"))
		if err != nil {
			pairs[key] = Evidence{"error": err.Error()}
			faults = append(faults, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		outside := []string{}
		for _, row := range rows {
			if f := strings.Fields(row); len(f) == 2 && (f[1] == "sync" || f[1] == "quorum") && remote[f[0]] && !local[f[0]] {
				outside = append(outside, f[0])
			}
		}
		pairs[key] = Evidence{"want": want, "synchronous_in_follower": outside, "replication": rows}
		if len(outside) < want {
			faults = append(faults, fmt.Sprintf("%s streams to %d synchronous standby(s) in a follower region, it declares %d: %v",
				key, len(outside), want, rows))
		}
	}
	if len(pairs) == 0 {
		return fmt.Errorf("writer %s has no database pair at dataDurability required; nothing is measured", writer.ID)
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}

// regionLossNames is the set of the names and instance names of the CNPG
// clusters of a list; with replicas only those with spec.replica.enabled.
func regionLossNames(list kube.Object, replicas bool) map[string]bool {
	names := map[string]bool{}
	for _, cl := range list.Objects("items") {
		if replicas && cl.Get("spec", "replica", "enabled") != true {
			continue
		}
		names[cl.Name()] = true
		instances, _ := cl.Get("status", "instanceNames").([]any)
		for _, n := range instances {
			if s, ok := n.(string); ok {
				names[s] = true
			}
		}
	}
	return names
}

// replication reads pg_stat_replication on the primary pod of a cluster, in a
// READ ONLY transaction, one row "<application_name> <sync_state>" a line.
func (c regionLossCheck) replication(ctx context.Context, r kube.Region, ns, pod string) ([]string, error) {
	if pod == "" {
		return nil, errors.New("the cluster names no current primary")
	}
	script := "BEGIN READ ONLY;\n" + regionLossReplSQL + ";\nROLLBACK;\n"
	out, err := c.kubectl(r, ctx, strings.NewReader(script), "exec", "-n", ns, pod, "-i", "-c", "postgres", "--",
		"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-d", "postgres")
	if err != nil {
		return nil, fmt.Errorf("pg_stat_replication of pod %s: %v", pod, err)
	}
	rows := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rows = append(rows, line)
		}
	}
	return rows, nil
}

func (c regionLossCheck) ownBao(ctx context.Context, r kube.Region, ev Evidence) error {
	s, err := secSealCheck{kubectl: c.kubectl}.status(ctx, r)
	if err != nil {
		return fmt.Errorf("bao status in %s/%s: %v", secBaoNS, secBaoPod, err)
	}
	ev["initialized"], ev["sealed"] = s.Initialized, s.Sealed
	if !s.Initialized || s.Sealed {
		return fmt.Errorf("OpenBao in %s/%s is initialised %v and sealed %v; its region cannot serve its own store", secBaoNS, secBaoPod, s.Initialized, s.Sealed)
	}
	return nil
}
