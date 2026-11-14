# SOV-090 Lab test: authoritative DNS and outbound mail on the reference cloud (result)

Date: 2026-11-13, two sessions. Type: lab test. Outcome: **DNS_MODE: delegated**. **MAIL_MODE: local**. All four rows are `yes`. No node port was used.

## Question

The test asks four questions on the reference cloud (Huawei Cloud Stack, one region, one availability zone):

- E1: Does PowerDNS answer on port 53, over UDP and TCP, on a public address? It must be reached through a host port on a node or a load-balancer listener, never a node port.
- E2: Does a zone delegated from a test parent zone resolve from the public internet?
- E3: Is a DNS-01 certificate issued through the Sovereign's own PowerDNS?
- E4: Is a mail sent from a pod to an outside mailbox, on port 25 or 587, delivered?

Decision rule: if E1, E2 and E3 are all `yes`, `DNS_MODE: delegated` becomes the default on this cloud. If any of them is `no`, the default stays `upstream-managed`. If E4 is `yes`, `MAIL_MODE: local` becomes the default. If E4 is `no`, the default is `relay`.

## What was run

### Session 1 (06:55 to 07:45 UTC): E1 and E4

- Read-only probes with signed REST calls: quotas, flavors, images, the DNS service, the reverse-record API, ELB flavors and listeners.
- One VPC, subnet, security group (53/UDP and 53/TCP from anywhere, ICMP, SSH from one client), key pair, elastic IP (5 Mbit/s, billed by traffic) and VM (s7n.medium.2, Ubuntu 22.04). All named `nslab-sov-090-*`, all deleted.
- On the VM: k3s v1.31.4+k3s1, PowerDNS Authoritative 5.0.3 (the image of the platform's PowerDNS chart) as a pod with `hostPort: 53` for UDP and TCP, and a mail test pod. Only ClusterIP services.
- Clients: a host that already exists in the region, a public dig web service, and check-host.net TCP checks from six countries.
- Mail: SMTP from the pod to a public disposable-mailbox service, and port 587/25 probes against large providers (no DATA to them).

### Session 2 (08:10 to 08:43 UTC): E2 and E3

- Read-only first:
  - The test-Organization pool zone `omani.homes` is served by the platform's own authoritative PowerDNS (the three platform name servers).
  - The platform's pool-domain manager writes to that zone through the PowerDNS HTTP API. It adds a child delegation as one NS record set (TTL 3600) by a PATCH of record sets.
  - The zone had no record under the lab label before the run.
  - The top-level domain publishes no DS record for the pool zone. The delegation is therefore not DNSSEC-validated.
- Lab resources, all named `nslab-sov-090-*` and all deleted at the end: one VPC, one subnet, one security group (same four rules as session 1), one key pair, one elastic IP (5 Mbit/s, billed by traffic), one VM (s7n.medium.2, Ubuntu 22.04, 40 GB SSD). OpenTofu created them in 2 min 45 s; the subnet alone took 1 min 59 s.
- On the VM:
  - k3s v1.31.4+k3s1 without traefik, servicelb and metrics-server. The node was Ready about 2 minutes after the VM.
  - PowerDNS Authoritative 5.0.3 as a pod with `hostPort: 53` for UDP and TCP. This time the host port was bound with `hostIP` to the node's private address.
  - No Service of type NodePort or LoadBalancer.
  - Child zone `nslab-sov-090.omani.homes`, created through the lab PowerDNS API: SOA, NS `ns1.nslab-sov-090.omani.homes`, an address record for `ns1` and for the apex, and a probe TXT record.
- Delegation: one PATCH to the pool zone through the platform's PowerDNS API. It added one NS record set (`nslab-sov-090` to `ns1.nslab-sov-090`, TTL 60) and its glue address record (TTL 60). It was live from 08:22:47 to 08:39:29 UTC.
- Certificate:
  - cert-manager v1.16.5, the version the platform's cert-manager blueprint pins.
  - The platform's own PowerDNS webhook chart, `bp-cert-manager-powerdns-webhook` 1.1.0, rendered from the repository source. Webhook image `zachomedia/cert-manager-webhook-pdns` v2.5.5.
  - The webhook pointed at the in-cluster PowerDNS API. Its key was generated in the cluster.
  - Only the chart's Let's Encrypt staging ClusterIssuer was applied.
  - One Certificate for the apex and the wildcard of the child zone, ECDSA P-256.
- Close-out: the delegation and glue were removed by one PATCH. The certificate was revoked and the ACME account deactivated. All cloud resources were destroyed.

## Measured values

| Row | Result | Session |
|---|---|---|
| E1 | yes (host-port path) | 1 |
| E2 | yes | 2 |
| E3 | yes | 2 |
| E4 | yes | 1 |

### E1: yes (session 1)

- From a client in the region, through the lab elastic IP: SOA over UDP and over TCP, NOERROR with the `aa` flag.
- A 1.3 KB TXT answer came back over UDP with the `tc` flag and complete over TCP (1321 bytes). PowerDNS limits UDP answers to 1232 bytes, so TCP must stay open.
- From the public internet: a public dig service got the SOA and the `_acme-challenge` TXT over UDP (about 210 ms). TCP connect to port 53 succeeded from 6 of 6 check nodes (0.10 to 0.30 s).
- The load-balancer listener path was not tested (elastic IP quota 10 of 10 at the time).
- Side effect: an unbound host port 53 also caught the node's loopback stub resolver. While the PowerDNS pod was not running, every name lookup on the node failed and all image pulls failed.

### E2: yes (session 2)

- Parent referral: the platform name servers answered the NS query for the label without the `aa` flag. The NS record was in the authority section and the glue address in the additional section.
- 9 s after the write, four public resolvers (Cloudflare, Quad9, OpenDNS and Google Public DNS) returned the probe TXT record from the child zone, over UDP.
- Over TCP, Cloudflare and Quad9 returned the apex address.
- A trace from the root went root, then the top-level domain, then the referral from a platform name server, then the answer from the lab server.
- check-host.net DNS check: 8 of 8 nodes (Austria, Hong Kong, Israel, Kazakhstan, Poland, Russia, United Kingdom, United States) returned the lab address.
- After removal: the three platform name servers answered NXDOMAIN at once. All four public resolvers answered NXDOMAIN 51 s later.

### E3: yes (session 2)

- Let's Encrypt staging issued the certificate. Issuer `(STAGING) Artificial Amaranth YE1`. Names: the apex and the wildcard of the child zone. Validity 64 days.
- The webhook wrote the `_acme-challenge` TXT records into the child zone through the in-cluster PowerDNS API and removed them afterwards. No challenge record was left in the zone.
- Timeline: request at 08:24:50, Ready at 08:38:07 (13 min 17 s).
  - 08:24:50 to 08:35:09: failed attempts (see below).
  - 08:35:51: first presentation in the child zone.
  - The apex challenge took 66 s and the wildcard challenge 65 s, one after the other.
  - Ready 2 min 16 s after the first correct presentation.
- Why the first attempts failed:
  - cert-manager finds the zone of the challenge name with SOA queries through recursive resolvers. It found the parent pool zone, not the child zone.
  - The webhook then asked the lab PowerDNS for the parent zone. It answered "Not Found". cert-manager retried for about 11 minutes.
  - Cause: a lab query for the child name, made less than a minute before the delegation was written, got NXDOMAIN. Resolvers keep that negative answer for the pool zone's SOA minimum, 3600 s.
  - 12 minutes after the delegation, Google Public DNS still answered NXDOMAIN for the child zone's SOA in 2 of 6 queries. The subnet's other default resolver answered correctly 6 of 6.
  - A restart of cert-manager did not help.
  - Setting cert-manager's DNS-01 resolvers to Cloudflare and Quad9 (with "recursive nameservers only") fixed it at once. The platform's cert-manager chart sets Google Public DNS and Cloudflare.

### E4: yes (session 1)

- From a pod: port 25 straight to the outside mailbox's MX, with STARTTLS: accepted in 1.85 s, delivered the same second.
- Port 587 to the Gmail and Microsoft 365 submission servers: EHLO, STARTTLS and login offered.
- Port 25 to the Gmail and Outlook MX: MAIL accepted.
- Reverse DNS: the elastic IPs have a generic reverse name from the network operator with no forward record. The DNS service's reverse-record API answered 404 `APIGW.0101`.

### Host port bound to the node address (session 2)

- With `hostIP` set to the node's private address, the host-port DNAT rules matched only that address.
- The node's stub resolver kept resolving public names while PowerDNS ran. All image pulls worked.
- Public queries to the elastic IP reached the pod, because the elastic IP maps 1:1 to the private address.
- This was measured with the k3s flannel portmap plugin only, not with Cilium.

### Other observations

- At start, PowerDNS 5.0.3 logs "PowerDNS Security Update Mandatory: Upgrade now" and points to security advisory 2026-05. The platform's PowerDNS chart pins 5.0.3.
- A zone created through the PowerDNS 5 API without an SOA gets the placeholder primary name `a.misconfigured.dns.server.invalid.`. The pool zone itself carries that placeholder as SOA primary, with a negative TTL of 3600 s.
- Lab capacity: session 1 waited 31 minutes for a free VPC slot. While the VPC quota was full, VPC creation answered HTTP 409 "conflict in the request". Session 2 found VPC 3 of 5 and elastic IP 7 of 10 at start, and did not wait.

## Decision

- E1, E2 and E3 are `yes`. So `DNS_MODE: delegated` is the default on this cloud. Check `SOV.07` does not report `fail:dns-upstream-managed`. Founder question 2 is not raised.
- E4 is `yes`. So `MAIL_MODE: local` is the default on this cloud.

## What follows for the plan

1. SOV-310 is no longer blocked by SOV-090. It still depends on SOV-150, CLD and NET.
2. SOV-310 write order: the parent must write the NS record set and glue before anything resolves a name in the child zone. One early lookup is cached as NXDOMAIN for the pool zone's negative TTL (3600 s). That blocks DNS-01 for up to an hour, and Google Public DNS, which the platform's cert-manager uses, held it longest. Lower the pool zones' SOA minimum (for example to 60 to 300 s), and make the child wait for the parent's write before it requests a certificate.
3. SOV-310 and CLD: bind host port 53 with `hostIP` set to the node's private address. That keeps the node's own resolver working and still serves the elastic IP. Check the same with Cilium's host-port handling on a Sovereign. The security group must open 53/UDP and 53/TCP. TCP is needed for answers above 1232 bytes.
4. SOV-310: the child's PowerDNS slot must write a valid SOA (primary `ns1.<child>`), because the PowerDNS 5 default is a placeholder. The pool zones carry the same placeholder; fixing them belongs to the parent's DNS owner.
5. Bump the PowerDNS 5.0.3 pin before SOV-310 exposes PowerDNS to the internet on every Sovereign. The image reports a mandatory security update.
6. The load-balancer listener path for port 53 is still not measured. The decision does not need it, because the host-port path is `yes`. Measuring it needs one free elastic IP.
7. SOV-320 may use `local`. But reverse names are generic and not forward-confirmed, and the cloud API cannot set them. Request a reverse name from the cloud operator for each Sovereign's mail address, or use `relay` for large mailbox providers. Port 587 is open, so `relay` works.
8. Lab scheduling: the VPC quota is 5 and elastic IPs run short. Queue lab tests that need a VPC, and treat HTTP 409 on VPC creation as "quota full, retry".

## Evidence

- Outcome: `DNS_MODE: delegated`, `MAIL_MODE: local`. E1 yes, E2 yes, E3 yes, E4 yes.
- Session 1, 07:28 to 07:45 UTC: VM created in 32 s, k3s in 62 s, everything destroyed in 85 s. Outside checks: one public dig-service query set (UDP) and one check-host.net TCP report from 6 nodes. The disposable mailbox was deleted (its login answers 401).
- Session 2, 08:14 to 08:43 UTC:
  - Delegation PATCH answered 204 at 08:22:47. A read-back showed exactly the NS set and the glue record.
  - Removal PATCH answered 204 at 08:39:29. The API read showed no record under the label, and the zone's record-set count was back to its starting value (138). All three platform name servers answered NXDOMAIN for the label and for the glue name.
  - check-host.net DNS reports: 8 of 8 nodes returned the lab address.
  - Certificate: cert-manager condition Ready=True at 08:38:07Z. The webhook logged "Presenting challenge" and "Cleaning challenge" with the resolved zone equal to the child zone.
  - ACME close-out: revocation answered 200. Account deactivation answered 200. A later account lookup answered 403 "account ... is deactivated".
  - OpenTofu destroyed 10 resources in 2 min 16 s. List calls for VPCs, subnets, security groups, elastic IPs, bandwidths, servers and key pairs found no name containing `nslab-sov-090`.
- No node port was used in either session. Each lab cluster had only ClusterIP services, and port 53 reached the pod through the host-port rule.

## Evidence

Outcome: **DNS_MODE: delegated. MAIL_MODE: local. All four rows yes: E1 yes and E4 yes (session 1), E2 yes and E3 yes (session 2). No node port was used.**. The measurements were made on the reference cloud on 13 November 2026.

- Session 1, 2026-11-13 06:55-07:28 UTC: read-only probes in the region with signed REST calls (quotas, flavors, images, DNS service, reverse-record API, ELB flavors and listeners, reverse DNS of elastic IPs)
- Session 1, 2026-11-13 07:27 UTC: port 25/587 probes and one port-25 test mail from a host that already exists in the region and has its own elastic IP (used only as a client)
- Session 1, 2026-11-13 07:28-07:45 UTC: one lab VM (s7n.medium.2, Ubuntu 22.04) with a new elastic IP, k3s v1.31.4+k3s1, PowerDNS Authoritative 5.0.3 as a pod on host port 53 UDP+TCP, and a mail test pod; created and destroyed with OpenTofu (huaweicloud provider ~> 1.65)
- Session 1, 2026-11-13 07:41-07:42 UTC: outside checks of the lab address: a public dig web service (UDP) and check-host.net TCP 53 from 6 nodes
- Session 2, 2026-11-13 08:10-08:14 UTC: read-only probes: VPC and elastic IP quotas (VPC 3 of 5, elastic IP 7 of 10), name servers and DS records of the pool zones, the pool zone through the platform's PowerDNS HTTP API (GET), and how the platform's pool-domain manager writes delegations (PATCH of record sets)
- Session 2, 2026-11-13 08:14-08:22 UTC: one lab VM (s7n.medium.2, Ubuntu 22.04) with its own VPC, subnet, security group, key pair and elastic IP, created with OpenTofu in 2 min 45 s; k3s v1.31.4+k3s1; PowerDNS Authoritative 5.0.3 as a pod with host port 53 UDP+TCP bound to the node's private address; child zone nslab-sov-090.omani.homes created through the lab PowerDNS API
- Session 2, 2026-11-13 08:22:47 UTC: one PATCH to the pool zone omani.homes through the platform's PowerDNS API: NS record set for nslab-sov-090 and its glue address record, both TTL 60
- Session 2, 2026-11-13 08:22:56-08:39:21 UTC: E2 checks: parent referral, four public resolvers over UDP, two over TCP, a trace from the root, and check-host.net DNS checks from 8 nodes
- Session 2, 2026-11-13 08:24:50-08:38:07 UTC: E3: cert-manager v1.16.5 and the platform's PowerDNS webhook chart (bp-cert-manager-powerdns-webhook 1.1.0, rendered from repository source) with its Let's Encrypt staging ClusterIssuer; one apex+wildcard Certificate issued by DNS-01 through the lab PowerDNS
- Session 2, 2026-11-13 08:39:29-08:43:23 UTC: close-out: delegation and glue removed by one PATCH and checked by API read and by queries to the three platform name servers; staging certificate revoked and ACME account deactivated; all cloud resources destroyed with OpenTofu and checked by list calls; working folder deleted

Resources the test created, and their end state:

- session 1: vpc nslab-sov-090-vpc: deleted
- session 1: subnet nslab-sov-090-subnet: deleted
- session 1: security group nslab-sov-090-sg (53/udp, 53/tcp, icmp, 22/tcp from one client): deleted
- session 1: key pair nslab-sov-090-key: deleted
- session 1: elastic IP nslab-sov-090-eip with bandwidth nslab-sov-090-bw (5 Mbit/s, traffic-billed): deleted
- session 1: ECS VM nslab-sov-090-vm (s7n.medium.2, 40 GB SSD; single-node k3s, PowerDNS pod, mail test pod): deleted
- session 1: third-party disposable mailbox: deleted (login now 401)
- session 1: working folder /home/openova/repos/tw-SOV-090: deleted
- session 2: vpc nslab-sov-090-vpc: deleted (VPC list has no match)
- session 2: subnet nslab-sov-090-subnet: deleted (subnet list has no match)
- session 2: security group nslab-sov-090-sg with 4 ingress rules (53/udp, 53/tcp, icmp, 22/tcp from one client): deleted (security-group list has no match)
- session 2: key pair nslab-sov-090-key: deleted (key-pair list has no match)
- session 2: elastic IP nslab-sov-090-eip with bandwidth nslab-sov-090-bw (5 Mbit/s, traffic-billed): deleted (elastic IP and bandwidth lists have no match)
- session 2: ECS VM nslab-sov-090-vm (s7n.medium.2, 40 GB SSD; single-node k3s with PowerDNS, cert-manager and the PowerDNS webhook): deleted (server list has no match)
- session 2: NS record set nslab-sov-090 in pool zone omani.homes (TTL 60): deleted (API read shows no record under the label; the three platform name servers answer NXDOMAIN)
- session 2: glue A record ns1.nslab-sov-090 in pool zone omani.homes (TTL 60): deleted (API read shows no record; the three platform name servers answer NXDOMAIN)
- session 2: Let's Encrypt staging ACME account: deactivated (lookup answers 403 deactivated)
- session 2: Let's Encrypt staging certificate for the lab zone: revoked (revokeCert answered 200)
- session 2: working folder /home/openova/repos/tw-SOV-090: deleted

## What later tasks must take into account

Both sessions together answer all four rows: E1 yes, E2 yes, E3 yes and E4 yes. So DNS_MODE: delegated and MAIL_MODE: local are the defaults on this cloud. SOV.07 does not fail and founder question 2 is not raised. SOV-310 is no longer blocked by SOV-090.

Later tasks must take into account:
(1) Write order in SOV-310. The parent must write the NS record set and glue before anything looks up a name in the child zone. One early lookup is cached as NXDOMAIN for the pool zone's negative TTL of 3600 s. In this run that blocked DNS-01 for about 11 minutes. Google Public DNS, which the platform's cert-manager uses with "recursive nameservers only", still gave NXDOMAIN 12 minutes after the delegation was written. Lower the pool zones' SOA minimum, and make the child wait for the parent's write before it requests a certificate.
(2) Bind host port 53 with hostIP set to the node's private address. That fixes the stub-resolver trap from session 1 and still serves the elastic IP, which maps 1:1 to that address. Measured with flannel portmap only; check the same with Cilium.
(3) The child PowerDNS must write a valid SOA. The PowerDNS 5 default primary is the placeholder a.misconfigured.dns.server.invalid. The pool zones carry the same placeholder.
(4) PowerDNS 5.0.3, the platform pin, logs a mandatory security update (advisory 2026-05). Bump the pin before SOV-310 exposes PowerDNS to the internet on every Sovereign.
(5) Let's Encrypt staging certificates are now valid for 64 days.
(6) The load-balancer listener path for port 53 is still not measured. It needs one free elastic IP.
(7) Mail (SOV-320): reverse names are generic and not forward-confirmed, and the reverse-record API answers 404 APIGW.0101. Request reverse names from the cloud operator, or use relay for large providers. Port 587 is open.
(8) The VPC quota is 5 and elastic IPs run short. Queue lab tests that need a VPC. HTTP 409 on VPC creation means the quota is full.

Session 2 took about 33 minutes. The working folder was deleted and the lab SSH key was shredded. The ACME account key was read only on the lab VM and shredded there. The cloud keys and the PowerDNS key were held only in shell variables.
