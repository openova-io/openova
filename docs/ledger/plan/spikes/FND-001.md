# FND-001 — Spike: the merge queue under release-writer pushes

Date: 2026-11-13. Lab repository: https://github.com/openova-io/north-star-merge-queue-lab (public, archived, kept as evidence). Raw logs are in its `evidence/` folder; the summary is `evidence/RESULTS.md`; the evidence commit is c4c9a4b.

Outcome: (b)
Identity kind: machine-user token with a ruleset bypass in mode "always" (measured); a deploy key is blocked by organisation policy; an application was not measured
Push starts workflows: yes
Checks red between the two commits: pin-match and probe, the lab checks that compare the two version sites (red on merge groups built on the bump commit, on a pull request opened in the window, and on the push of the bump commit)

## The question

What happens to a merge queue on `main` when the release writer pushes version-bump commits straight to `main` while pull requests wait in the queue? Who can make such a push at all? What does one merge cost? Which checks go red between a bump commit and the commit that moves the pins?

## What was run

- A throwaway public repository in the organisation, created for this spike only. No platform code and no secret went into it. The platform repository was not changed.
- Ruleset on `main`, as the design asks: merge queue with squash, up to 5 groups built at once, up to 5 pull requests per group, minimum group size 1, wait 5 minutes, all entries green, 60-minute check timeout. Required checks `dummy-5`, `dummy-15`, `dummy-30`, `pin-match` (not strict). Pull request required, zero approvals, code-owner review on. No force push. No deletion.
- Ruleset on `rc/**`: creation, update and deletion restricted to the bypass identity.
- Check durations are scaled down ten times: 30, 90 and 180 seconds stand for 5, 15 and 30 minutes. The limit "twice the longest workflow" is therefore 360 seconds.
- The timed checks are commit statuses posted by a script on the host, not Actions jobs. The organisation's hosted-runner pool was saturated (see "Runner pool"); Actions jobs would have measured runner waits, not the queue. The queue treats a required commit status like a required check run.
- One real Actions workflow, `probe`, runs on `pull_request`, `merge_group` and pushes to `main`. It records the event and the pusher and compares the two version files. It is not required.
- `pin-match` and `probe` compare two files, `release/chart-version` and `release/slot-pin`. The release writer moves them either in one commit, or in two commits one after the other (bump, then pin), as the design's two-commit path does.
- Five runs, A to E, with six to nine small pull requests each. The release writer was played by a script that pushes with a user token that has the bypass.
- Time box: the spike ran in about 90 minutes.

## Who can push to a `main` that requires the queue

- A user token of an organisation owner with no bypass: refused, GH013, "Changes must be made through the merge queue".
- The workflow token (`GITHUB_TOKEN`): refused, GH013 (Actions run 43380240904).
- The GitHub Actions app as a bypass actor: the ruleset API refuses it with HTTP 422, "Actor GitHub Actions integration must be part of the ruleset source or owner organization". The workflow token cannot be the release writer.
- A deploy key: the ruleset accepts a deploy-key bypass entry, but no deploy key can be added. The API answers "Deploy keys are disabled for this repository". The organisation setting that allows deploy keys is off. Turning it on is an organisation-wide security decision, so it was not changed for this spike.
- A user token with a repository-role bypass in mode `pull_request`: refused, GH013.
- A user token with a repository-role bypass in mode `always`: accepted, "Bypassed rule violations for refs/heads/main" (commit 7359e32).
- On `rc/**`: refused without the bypass ("creations being restricted"), accepted with it.
- A GitHub App was not tried. Creating one needs a web step by an organisation owner; there is no API for it.

## Does a push start the workflows that listen for pushes

Yes, for every accepted push.

- Every user-token push started the push workflows. The first one is Actions run 43380856047: actor, pusher and sender are the pushing account, sender type `User`.
- Every queue merge started them too, with actor `github-merge-queue[bot]` (for example run 43382239369).
- The queue merges by moving `main` to the group commit. Five pull requests merged together made one push event (commit 98e1976).

## The runs

Time in queue runs from "added to the merge queue" to "merged", read from each pull request's timeline.

| Run | Pull requests | Release writer | Writer commits | Median | Range | Groups built | Removed for failed checks |
|---|---|---|---|---|---|---|---|
| A | #1–#6, all added at once | none | 0 | 229.5 s | 220–436 s | 6 | 0 |
| B | #7–#12, all at once | one consistent commit (6baf4cb), 80 s into the groups | 1 | 333 s | 326–539 s | 11 | 0 |
| C | #13–#18, all at once | one consistent commit 60 s after each merge | 2 | 230 s | 225–508 s | 7 | 0 |
| D | #19–#26, one per minute; #27 opened in the window | a bump commit 60 s after each merge, the pin commit 60 s later | 8 | 362 s (the 8 pull requests); 330 s with #27 | 207–936 s | 37 for 9 | 3 |
| E | #28–#35, one per minute | two consistent commits, 60 s and 120 s after each merge | 6 | 446.5 s | 224–638 s | 27 | 0 |

- Run A has no pushes. Its median, 229.5 s for a 180 s check, shows about 50 s of fixed overhead from the lab's status poster.
- Run E is the case that remains when every writer commit is consistent: a release commit and a deploy bump after each merge. Its median is 446.5 s, 2.5 times the longest check. Without the 50 s lab overhead it is still about 397 s, 2.2 times.
- Run D is the two-commit design as it stands. Its median is 362 s, at the limit. Without the lab overhead it is under it. But it also removed pull requests from the queue three times (see below), and the times include re-adding them within 1.5 to 3.6 minutes, faster than an executor would.
- Both steady-inflow runs used fewer writer commits per merged pull request (0.89 in D, 0.75 in E) than the platform makes today. A real rate makes them slower, not faster.
- Scaled back to real time, the baseline median is about 38 minutes for a 30-minute workflow; run D is about 60 minutes; run E is about 74 minutes.

## What a push does to the queue

- Every writer push made while groups were in flight dropped all of them: 14 of 14 pushes. The queue rebuilt the groups on the new `main` within 3 to 21 seconds. All work already done by the dropped groups is lost.
- The entries keep their place. After a consistent push a pull request needs nothing: no re-run, no branch update, no re-enqueue.
- Actions runs of dropped groups are not cancelled. The job of group 4225973 started at 07:34:31, four minutes after its group was dropped at 07:30:29, and ran to the end. Every rebuild therefore adds jobs to the runner pool.

## The window between the bump commit and the pin commit

- A group built on a bump commit fails the comparing check. The queue then removes the first pull request of the failing groups with the reason `failed_checks`, marks the others unmergeable and rebuilds them. This happened three times: #21 at 07:31:02 and again at 07:38:38, #22 at 07:33:09.
- A removed pull request must be added to the queue again by hand. That was accepted at once; its own checks did not need to run again.
- A pull request opened in the window (#27, opened 07:30:17) gets a red `pin-match` on its own head ("chart-version=1.0.5 slot-pin=1.0.4") and a red `probe` run. It cannot be added to the queue: "Required status check pin-match is failing". It needed a branch update (07:35:03) and a full new check cycle before it could be added (07:39:45). It merged at 07:43:39, 13 minutes after it was opened, of which only 233 s were in the queue.
- The `probe` push run failed on all four bump commits pushed to `main` (d0367c8, b746926, 599670b, 241d321). 19 merge-group runs of `probe` failed.
- In the platform the matching checks were not run. Read from its `main` on the day of the spike, the pull-request workflows that read the version sites are `check-catalog-seed-lockstep`, `check-chart-version-forward`, `test-bootstrap-kit`, `check-catalog-generated-drift`, `cluster-template-drift`, `check-chart-version-bumped` and `check-release-lockstep-writer`. All seven are path-filtered and none listens for `merge_group`. So in W0 they cannot remove a pull request from the queue, but they are candidates to go red on a pull request that touches their paths and is checked during the window. A required check through the aggregate check would then block that pull request until a branch update. This is not measured.

## Other measured points

- Actions runs on every merge-group commit: 88 `merge_group` runs, one per group head, on refs `gh-readonly-queue/main/pr-<n>-<base sha>`. The run's actor is the account that added the pull request to the queue.
- With the setting "pull request title and description", the squash commit carries the whole pull-request body. The line `Work-Package: LAB-a-1` arrived in the merge-group commit and on `main` (commit 942f2be). GitHub appends its own `Co-authored-by:` paragraph after the body, so a line in the body is not in the last paragraph and a trailer parser that reads only the last paragraph misses it.
- With a minimum group size of 1, the "wait 5 minutes" setting never takes effect.
- A bypass push prints the rules it bypassed. That record exists for every writer push.

## Runner pool

The organisation is on the free plan: 20 hosted jobs at once, shared by all its repositories. Of 134 lab jobs that finished, the median wait for a runner was 79 s, the 75th percentile 362 s, the 90th percentile 609 s and the longest 806 s. At the end 26 lab jobs had waited up to 15 minutes without a runner; they were cancelled. Every queue rebuild adds merge-group jobs to this pool, and dropped groups keep their jobs.

## What follows for the plan

- Outcome (b): groups rebuild on every writer push, and with steady inflow the median time in queue is more than twice the longest workflow. FND-143 (release-writer commits batched at the cut) moves into W0, directly after FND-013, so the writer pushes only at the cut. FND-016 keeps its own condition.
- FND-011: a required check that compares the two sites went red between the two commits and removed pull requests from the queue. Move the bump into the release writer so that all five sites land in one commit. The architect amends FND-011 before it starts.
- FND-017, identity: the workflow token cannot bypass. A deploy key needs the organisation setting for deploy keys turned on, and it does not fit a script that pushes over HTTPS with a token and checks a login. The measured working kind is a token of a dedicated machine account with a bypass in mode `always`. Give that bypass to a team with that one member, not to the admin repository role, which would let every administrator bypass. An application is the alternative; it needs one re-run of this spike after an organisation owner creates it.
- FND-017, triggers: a push by such an identity starts the workflows that listen for pushes. FND-017 deletes the hand dispatch of the release workflow.
- FND-011's push guard: the queue's own merges arrive as pushes with actor `github-merge-queue[bot]`. The guard must treat that actor as the queue, not as a breach.
- FND-013: name the minimum group size, or drop the wait time, because with a minimum of 1 the wait does nothing. Account for runs of dropped groups, which are not cancelled, on a pool where jobs already wait minutes.
- The reader of work-package trailers (FND-022) can rely on the pull-request body reaching the merge-group commit, but must search the whole message, not only the last paragraph.

## Evidence

Outcome: **b**. The experiment ran on a throwaway repository of the organisation, archived and kept as evidence.

- Lab repository (public, archived): https://github.com/openova-io/north-star-merge-queue-lab — evidence commit c4c9a4b, summary evidence/RESULTS.md, raw logs evidence/
- Rulesets: main id 27182199 (merge queue + required checks + PR + no force push + no deletion), rc id 27182200 (rc/** creation/update/deletion restricted)
- Probe P1 (user token, no bypass): refused GH013 — evidence/probes.log
- Probe P2 (workflow token push): refused GH013 — Actions run 43380240904
- Probe P4 (user token, repository-role bypass, mode pull_request): refused GH013 — evidence/probes.log
- Probe P5 (user token, repository-role bypass, mode always): accepted, commit 7359e32; push workflows started — Actions run 43380856047
- Queue merge push starts push workflows, actor github-merge-queue[bot] — Actions run 43382239369
- Run A (baseline, no writer): PRs #1-#6
- Run B (one consistent writer commit 6baf4cb mid-flight): PRs #7-#12
- Run C (one consistent commit 60 s after each merge, burst enqueue): PRs #13-#18
- Run D (bump commit then pin commit after each merge, one enqueue per minute): PRs #19-#26, window PR #27
- Run E (two consistent commits after each merge, one enqueue per minute): PRs #28-#35
- Dropped group not cancelled: merge_group job for group 4225973 started 4 min after the group was dropped — evidence/actions-runs.tsv

## What later tasks must take into account

Outcome (b). Later tasks must take into account:

1. Identity. FND-017 cannot use the workflow token: GitHub refuses it as a bypass actor. Deploy keys are switched off by an organisation setting ("Deploy keys are disabled for this repository"). I did not change that setting, because it applies to the whole organisation and is the founder's security decision. The only kind measured to work is a user token with a bypass in mode "always". Mode "pull_request" is refused. A GitHub App was not measured: creating one needs a web step by an organisation owner. If FND-017 prefers an app, re-run this lab once the app exists.

2. Every writer push drops all groups in flight. This happened for 14 of 14 pushes. The entries stay in the queue and are rebuilt within 3 to 21 s.

3. The two-commit window removes pull requests from the queue (reason failed_checks) when a required check compares the version sites. A pull request opened during the window cannot be added until its branch is updated and all its checks run again. So FND-011 must move all five sites into one commit.

4. Batching (FND-143) moves into W0 directly after FND-013. Even with consistent single commits and steady inflow, the median time in queue was 2.5 times the longest check (446.5 s against a limit of 360 s), at a writer rate below the platform's.

5. A push by the bypass identity, and every merge by the queue, starts the push workflows. FND-017 therefore deletes the hand dispatch of the release workflow. FND-011's push guard must also accept actor github-merge-queue[bot] as a merge by the queue.

6. Squash commits carry the pull-request body, but GitHub appends its own Co-authored-by paragraph after it. The trailer reader must search the whole message. With a minimum group size of 1, the 5-minute wait in FND-013 does nothing.

7. Answer to the throughput question. The organisation's free-plan runner pool (20 jobs at once, shared by every repository and every parallel spike) is a real serialization point. Lab jobs waited up to 13 to 15 minutes for a runner. Runs of dropped merge groups are not cancelled. I therefore posted the timed checks as commit statuses from a host script and kept one real Actions workflow to cover merge_group and push behaviour.

8. Housekeeping. The repository and the rulesets were created with the default hatiyildiz profile; the emrahbaysal fallback was not needed. The repository is archived, not deleted. The local folder /home/openova/repos/tw-FND-001 is deleted and the unused SSH key was shredded. The platform repository was only read.

9. Acceptance file name. The acceptance commands A1 to A3 check docs/ledger/plan/spikes/FND-001-merge-queue.md, while this task names FND-001.md. Commit the record under the acceptance name, or under both names. Its four marker lines each appear exactly once.

10. Host. During the run /home briefly reached 100 % full and DNS lookups for github.com failed a few times. Other parallel tasks on this host may hit the same problems.
