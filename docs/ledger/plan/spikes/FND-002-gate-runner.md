# FND-002 result: from where a gate runner reaches every region

- Date: 2026-11-13
- Environment: hw307, two regions (A and B), past its cutover
- The gate runner is placed in the mothership's network. From there every region is reachable:

Outcome: reachable


## Question

The gate must run its checks against every region of an environment. From which place can one runner reach the Kubernetes API of every region?

## What was run

One request to `<API server>/version` of each region, without credentials, with a time limit of 8 seconds, from four places. An answer `401` means the API server was reached and asked for credentials. No answer within 8 seconds means it was not reached. Nothing was changed on any cluster: the requests were sent from pods that were already running, and from the host.

## Measured values

| From | Region A's API | Region B's API |
|---|---|---|
| The host that runs the agents (in the cloud's network) | 401 in 0.005 s | 401 in 0.005 s |
| A pod of the mothership (another provider's network) | 401 in 0.83 s | 401 in 0.80 s |
| A pod in region A | its own API through the cluster address: 401 in 0.006 s | no answer in 8 s |
| A pod in region B | not measured: no running pod with a request tool was at hand | not measured |

Both API servers listen on a public address of their region, on port 6443.

## What follows

1. A runner in the mothership's network reaches every region of an environment. That is the default place of the gate runner, and it holds.
2. A runner inside one region does not reach the other region's API on its public address. A design that runs the gate from inside the environment needs one runner per region, or a path through the mesh.
3. Not measured: a runner hosted by GitHub. The API servers are on public addresses, so the network path may exist, but a hosted runner would also need the credentials of the environment outside the organisation's own machines. The default in point 1 avoids that.
4. The measurement was made on one environment. A new environment must show the same from the mothership before its first gate run; the gate's own first check does that.

## Evidence

Outcome: **reachable** (from a runner in the mothership's network). The four measurements above were made by hand by the lead architect on 13 November 2026 between 06:40 and 06:45 UTC; there is no CI run for them.

## What later tasks must take into account

The runner of the gate is a machine or pod in the mothership's network (FND-011 and the tasks after it). It is not a pod of the environment under test. A check that must see both regions is run from there, with one kubeconfig per region.
