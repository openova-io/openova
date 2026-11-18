{{/*
The allow files of bp-network-policies. This file renders nothing.

A namespace under default deny (chart value enforcedNamespaces, object
baseline-default-deny) receives only what its allow object lets in, and sends
only DNS to the cluster resolver and what its allow object lets out. The
allow object of namespace <namespace> is written in this folder, in the file
<namespace>.yaml, and in no other place.

THE RULE TEMPLATE. A whole file, for a namespace named gitea (the ports and
peers are an illustration of the form, not the rules of that namespace):

  {{- define "allow.gitea.rules" }}
  ingress:
    - fromEntities: [ingress]
      toPorts: [{ports: [{port: "3000", protocol: TCP}]}]
    - fromEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: flux-system}}]
      toPorts: [{ports: [{port: "3000", protocol: TCP}]}]
  egress:
    - toEndpoints: [{matchLabels: {io.kubernetes.pod.namespace: shared-data}}]
      toPorts: [{ports: [{port: "5432", protocol: TCP}]}]
    - toFQDNs: [{matchName: "git.upstream.example"}]
      toPorts: [{ports: [{port: "443", protocol: TCP}]}]
  {{- end }}
  {{- include "network-policies.allow" . }}

The template is named allow.<namespace>.rules and the file <namespace>.yaml.
The helper network-policies.allow (_helpers.tpl) renders the object
allow-<namespace> from them, and only while the namespace is in
enforcedNamespaces. It adds the selector, the pods of the namespace that are
not session pods (label access.openova.io/session), and same-namespace
traffic in both directions; the file writes neither.

The rules of an allow file:
  1. One object per namespace, one file per object.
  2. Every rule names a peer and at least one port, and no other key. A peer
     is a namespace, by the label io.kubernetes.pod.namespace in each endpoint
     selector; one of the entities ingress, kube-apiserver, host and
     remote-node; a range; or, for egress, a DNS name.
  3. A webhook or conversion server admits kube-apiserver and remote-node on
     its webhook port.
  4. Internet egress is a toFQDNs rule or a toCIDR list taken from the cloud
     profile. Forbidden: every other entity (world, world-ipv4, world-ipv6,
     all, cluster among them), a range wider than /8 (IPv4) or /16 (IPv6), a
     DNS wildcard other than one leading *. or **. before a name that holds
     a dot, the port 0 in any spelling (a port is a number from 1 to 65535
     in decimal without a leading zero), and the range from the port 1 to
     the endPort 65535. The helper stops the render on each of them.
  5. The rules come from the flows the namespace really has, measured, not
     from what its chart might do.

THE STANDING RULE. A change that adds a kit namespace, or that adds a flow to
a namespace in enforcedNamespaces, carries in its own pull request:
  - the allow file of that namespace, new or changed, by the rule template;
  - for a new namespace, its entry in enforcedNamespaces;
  - one row with expect allow (a flow its workload needs) and one row with
    expect deny (a dial from the namespace default to its main port) in the
    dial table tests/acceptance/sovereign/policy-dials.yaml, which check
    NET-G06 (tests/acceptance/sovereign/internal/suite/check_policy_dials.go)
    dials in every region.
The chart test tests/render.sh renders every file of this folder: it must
render nothing while its namespace is not enforced and exactly one object,
allow-<namespace>, while it is.
*/}}
