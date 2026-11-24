"""The worst-case inputs of this module's user data, for scripts/cloudinit-size-check.py (#7893).

The program calls worst_case(c, limit) with its own module as c (the fillers
and the lengths shared by every provider are there) and the user-data limit
of this pack's profile.yaml as limit (spec.bootstrap.userDataLimitBytes, the
one place of the cloud's limit), and renders this module's
user-data locals with what it returns: the module variables, the resource
attributes the locals read (`res`), stand-ins for locals derived from
resources, the locals to render and the surfaces to measure with their limits.
"""

# The FQDN. The object-storage bucket name rule allows 45 characters (see
# scripts/cloudinit-size-check.py), and here a second rule is tighter: the
# huaweicloud provider refuses a huaweicloud_vpc_peering_connection name over
# 64 characters, and main.tf names the region-to-region peering
# `${name_prefix}-peer-<8 hex>` with name_prefix = `catalyst-<FQDN with
# dashes>-<8 hex>`: 64 - 6 - 8 - 9 - 1 - 8 = 32 characters.
FQDN_LENGTH = 32

# The limit. The ECS API refused 43,908 base64 characters against a quota of
# 43,692, the base64 form of 32,769 bytes (fresh two-region installation of
# 14 Nov 2026, #7893): the limit is 32 KiB of raw user data. It stands in
# profile.yaml and arrives here as `limit`. The module has no precondition of
# its own, so the API is the limit for every surface.


def worst_case(c, limit):
    fqdn = c.fqdn(FQDN_LENGTH)
    regions = [
        {"code": "me-east-215-a", "cloud_region": "me-east-215", "role": "primary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": 50},
        {"code": "me-east-215-b", "cloud_region": "me-east-215", "role": "secondary", "control_plane_size": "s7n.large.4", "worker_size": "m7n.xlarge.8", "worker_count": 50},
    ]
    variables = {
        "deployment_id": c.DEPLOYMENT_ID,
        "sovereign_fqdn": fqdn,
        "pdns_api_host": "https://pdns." + c.PARENT_FQDN,
        "regions": regions,
        # The provisioner's Huawei form (provisioner.go: `{name: %q, role: %q}`).
        "parent_domains_yaml": "[" + ", ".join(
            [f'{{name: "{fqdn}", role: "primary"}}'] + [f'{{name: "{d}", role: "org-pool"}}' for d in c.ORG_POOLS]) + "]",
        "handover_jwt_public_key": c.HANDOVER_JWK,
        # Huawei IAM access key id: 20 upper-case letters and digits; secret: 40 characters.
        # The key id filler starts with HWAKFILLER so that it looks like no real key id.
        "huawei_access_key": "HWAKFILLER" + c.filler("huawei-ak", 10, c.UPPER_ALNUM),
        "huawei_secret_key": c.filler("huawei-sk", 40, c.ALNUM),
        "huawei_project_id": c.filler("huawei-project", 32, c.HEX),
        "ssh_public_key": c.SSH_KEY,
        "org_name": c.ORG_NAME,
        "org_email": c.ORG_EMAIL,
        "ghcr_pull_token": c.GHCR_TOKEN,
        "kubeconfig_bearer_token": c.filler("bearer", c.TOKENS["bearer"], c.HEX),
        "catalyst_api_url": c.CATALYST_API_URL,
        # Required since CLD-090 (contract version 1): the durable class the
        # module defaulted to, and the GitOps repository of the variables document.
        "default_storage_class": "evs-ssd",
        "gitops_repo_url": c.variables_doc(fqdn, c.PARENT_FQDN, c.CATALYST_API_URL)["GITOPS_REPO_URL"],
        "harbor_robot_token": c.filler("harbor", c.TOKENS["harbor"], c.ALNUM),
        "powerdns_api_key": c.filler("powerdns", c.TOKENS["powerdns"], c.ALNUM),
        "pdm_basic_auth_user": c.filler("pdm-user", c.TOKENS["pdm_user"], c.ALNUM),
        "pdm_basic_auth_pass": c.filler("pdm-pass", c.TOKENS["pdm_pass"], c.ALNUM),
        "obs_bucket_name": "catalyst-" + fqdn.replace(".", "-") + "-" + c.DEPLOYMENT_ID[:8],
        "marketplace_enabled": "true",
        "console_isolation_enabled": "true",
        "bcp_topology": "active-hotstandby",
        "enable_hot_standby": "true",
        "enable_shared_pg": "true",
        "wildcard_cert_use_staging": "true",
        "retry_attempt": 9,
        "qa_fixtures_enabled": "true",
        "variables": c.variables_doc(fqdn, c.PARENT_FQDN, c.CATALYST_API_URL),
        "provider_api_cidrs": c.PROVIDER_API_CIDRS,
    }
    codes = [r["code"] for r in regions]
    res = {
        "huaweicloud_vpc_eip": {
            "cp": {code: {"publicip": [{"ip_address": c.ipv4("eip-cp-" + code)}]} for code in codes},
            "elb_primary": {"publicip": [{"ip_address": c.ipv4("eip-elb")}]},
            "elb_console": [{"publicip": [{"ip_address": c.ipv4("eip-console")}]}],
        },
        "random_password": {
            "k3s_server_token": {code: {"result": c.filler("k3s-server-" + code, c.TOKENS["k3s"], c.ALNUM)} for code in codes},
            "k3s_agent_token": {code: {"result": c.filler("k3s-agent-" + code, c.TOKENS["k3s"], c.ALNUM)} for code in codes}, "keycloak_client_secret_salt": {"result": c.filler("keycloak-salt", 48, c.ALNUM)},
        },
    }
    # The workers' join address: the DHCP address of the region's first control
    # plane (huaweicloud_compute_instance.control_plane[...].access_ip_v4), of
    # the longest form in its /24.
    standins = {"cp_primary_private_ip_by_region": {code: f"10.{247 - i}.1.254" for i, code in enumerate(codes)}}
    roots = ["cp_cloud_init_by_region", "worker_cloud_init_by_region"]

    def surfaces(out):
        return [
            ("control-plane, primary region (worst case)", out["cp_cloud_init_by_region"][codes[0]], limit),
            ("control-plane, secondary region (worst case)", out["cp_cloud_init_by_region"][codes[1]], limit),
            ("worker, primary region (worst case)", out["worker_cloud_init_by_region"][codes[0]], limit),
            ("worker, secondary region (worst case)", out["worker_cloud_init_by_region"][codes[1]], limit),
        ]
    return variables, res, standins, roots, surfaces
