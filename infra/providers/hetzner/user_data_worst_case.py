"""The worst-case inputs of this module's user data, for scripts/cloudinit-size-check.py (#7893).

The program calls worst_case(c) with its own module as c (the fillers and the
lengths shared by every provider are there) and renders this module's
user-data locals with what it returns: the module variables, the resource
attributes the locals read (`res`), stand-ins for locals derived from
resources, the locals to render and the surfaces to measure with their limits.
"""
import json

# The FQDN: 45 characters, the most the object-storage bucket name rule allows
# (see scripts/cloudinit-size-check.py).
FQDN_LENGTH = 45

# The limits. The API's hard cap is 32,768 bytes (main.tf, issue #966), and
# this module refuses less at plan time: the primary control plane 32,576, a
# secondary control plane 32,256, a worker 30,720 (its preconditions). The
# smaller of the two is what a real installation can send.
LIMIT_CP_PRIMARY = 32576
LIMIT_CP_SECONDARY = 32256
LIMIT_WORKER = 30720


def worst_case(c):
    fqdn = c.fqdn(FQDN_LENGTH)
    regions = [
        {"code": "fsn1", "cloud_region": "fsn1", "role": "primary", "control_plane_size": "ccx63", "worker_size": "ccx63", "worker_count": 50},
        {"code": "nbg1", "cloud_region": "nbg1", "role": "secondary", "control_plane_size": "ccx63", "worker_size": "ccx63", "worker_count": 50},
    ]
    slug = fqdn.replace(".", "-")
    variables = {
        "sovereign_fqdn": fqdn,
        "sovereign_subdomain": fqdn.split(".")[0],
        "sovereign_deployment_id": c.DEPLOYMENT_ID,
        "deployment_id": c.DEPLOYMENT_ID,
        "marketplace_enabled": "true",
        "console_isolation_enabled": "true",
        "bcp_topology": "active-hotstandby",
        "enable_hot_standby": "true",
        "enable_shared_pg": "true",
        "qa_fixtures_enabled": "true",
        "wildcard_cert_use_staging": "true",
        "variables": c.variables_doc(fqdn, c.PARENT_FQDN, c.CATALYST_API_URL),
        # The provisioner's JSON form (provisioner.go parentDomainsYAMLLiteral).
        "parent_domains_yaml": json.dumps([{"name": fqdn, "role": "primary"}] + [{"name": d, "role": "org-pool"} for d in c.ORG_POOLS],
                                          separators=(",", ":")),
        # provisioner.go DeriveClusterMeshName: <first FQDN label>-mesh for more than one region.
        "cluster_mesh_name": fqdn.split(".")[0] + "-mesh",
        "cluster_mesh_id": 1,
        "org_name": c.ORG_NAME,
        "org_email": c.ORG_EMAIL,
        # Hetzner Cloud API token: 64 characters.
        "hcloud_token": c.filler("hcloud", 64, c.ALNUM),
        "hcloud_project_id": "12345678",
        "region": "fsn1",
        "control_plane_size": "ccx63",
        "worker_size": "ccx63",
        "worker_count": 50,
        "regions": regions,
        "ssh_public_key": c.SSH_KEY,
        "ghcr_pull_token": c.GHCR_TOKEN,
        "powerdns_api_key": c.filler("powerdns", c.TOKENS["powerdns"], c.ALNUM),
        "kubeconfig_bearer_token": c.filler("bearer", c.TOKENS["bearer"], c.HEX),
        "catalyst_api_url": c.CATALYST_API_URL,
        # Required since CLD-090 (contract version 1): the durable class the
        # module defaulted to, and the GitOps repository of the variables document.
        "default_storage_class": "hcloud-volumes",
        "gitops_repo_url": c.variables_doc(fqdn, c.PARENT_FQDN, c.CATALYST_API_URL)["GITOPS_REPO_URL"],
        "object_storage_region": "fsn1",
        # The longest the module's validation allows: access key 64, secret key 128 characters.
        "object_storage_access_key": c.filler("obs-ak", 64, c.ALNUM),
        "object_storage_secret_key": c.filler("obs-sk", 128, c.ALNUM),
        "harbor_robot_token": c.filler("harbor", c.TOKENS["harbor"], c.ALNUM),
        "pdm_basic_auth_user": c.filler("pdm-user", c.TOKENS["pdm_user"], c.ALNUM),
        "pdm_basic_auth_pass": c.filler("pdm-pass", c.TOKENS["pdm_pass"], c.ALNUM),
        "object_storage_bucket_name": "catalyst-" + slug + "-" + c.DEPLOYMENT_ID[:8],
        "handover_jwt_public_key": c.HANDOVER_JWK,
        "provider_api_cidrs": c.PROVIDER_API_CIDRS,
    }
    secondary_key = "nbg1-1"
    res = {
        "hcloud_load_balancer": {
            "main": {"ipv4": c.ipv4("lb-main")},
            "console": [{"ipv4": c.ipv4("lb-console")}],
            "secondary": {secondary_key: {"ipv4": c.ipv4("lb-secondary")}},
        },
        # The names the module gives these resources (hcloud_network.region,
        # hcloud_firewall.main, hcloud_ssh_key.main).
        "hcloud_network": {"region": {k: {"name": f"catalyst-{slug}-{k}-net"} for k in ["primary", secondary_key]}},
        "hcloud_firewall": {"main": {"name": f"catalyst-{slug}-fw"}},
        "hcloud_ssh_key": {"main": {"name": f"catalyst-{slug}"}},
        "random_password": {
            "k3s_server_token": {k: {"result": c.filler("k3s-server-" + k, c.TOKENS["k3s"], c.ALNUM)} for k in ["primary", secondary_key]},
            "k3s_agent_token": {k: {"result": c.filler("k3s-agent-" + k, c.TOKENS["k3s"], c.ALNUM)} for k in ["primary", secondary_key]},
        },
    }
    standins = {}
    roots = ["control_plane_cloud_init", "secondary_region_cloud_init", "worker_cloud_init", "secondary_region_worker_cloud_init"]

    def surfaces(out):
        return [
            ("control-plane, primary region (worst case)", out["control_plane_cloud_init"], LIMIT_CP_PRIMARY),
            ("control-plane, secondary region (worst case)", out["secondary_region_cloud_init"][secondary_key], LIMIT_CP_SECONDARY),
            ("worker, primary region (worst case)", out["worker_cloud_init"], LIMIT_WORKER),
            ("worker, secondary region (worst case)", out["secondary_region_worker_cloud_init"][secondary_key], LIMIT_WORKER),
        ]
    return variables, res, standins, roots, surfaces
