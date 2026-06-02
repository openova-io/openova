#!/usr/bin/env python3
"""G117.1 Wave-1 B3 — Application-tier blueprint topology+endpoints+sso+multiInstance decls.

Produces the four new YAML blocks for each of the 29 Application-tier Blueprints
named in the audit doc (docs/sessions/2026-06-02-per-blueprint-topology-audit.md).

Output: a dict keyed on bp-name → {topology, endpoints, sso, multiInstance}.

This module is the single source of truth for the per-blueprint values; the
appender script imports it.

Per the briefing:
  - These live INSIDE per-Org vClusters
  - perTopology.<choice>.placement.tier: per the audit (rtz for almost all)
  - multiInstance.allowed: true for most (multiple instances per Org allowed)
  - Stateful-replicated (matrix, langfuse, temporal, wordpress) → active-passive default
    with cnpg-pair sync
  - Stateless A/A (livekit, stunner, vllm, bge, llm-gateway, etc.) → active-active default
  - stalwart-sovereign is external (mothership-hosted) → custom 'external' is not in the
    enum; we document by setting supported: [singleton] + placement.tier: '' + a comment
    in the blueprint.yaml file (the audit-doc row already calls it external)
"""

from __future__ import annotations

# Per-blueprint declarations. Keys are bp-names (with the bp- prefix matching the audit doc).
# Each value is a dict that becomes the 4 new YAML blocks in the per-blueprint file.

# Common patterns:
def _active_passive_cnpg_sync(name: str, hostname: str = None, port: int = 443,
                              protocol: str = "https", sso_enabled: bool = False) -> dict:
    """Standard stateful-replicated Application Blueprint: CNPG pair sync, bp-continuum."""
    if hostname is None:
        hostname = f"{name}.{{{{.OrgSlug}}}}.{{{{.SovereignFQDN}}}}"
    return {
        "topology": {
            "supported": ["active-passive", "singleton"],
            "defaults": {
                "multi-region": "active-passive",
                "single-region": "singleton",
            },
            "perTopology": {
                "active-passive": {
                    "replication": {
                        "backend": "cnpg-pair",
                        "mode": "sync",
                        "lagSloSeconds": 0,
                    },
                    "switchover": {
                        "mechanism": "bp-continuum",
                        "rtoSeconds": 60,
                        "rpoSeconds": 0,
                    },
                    "placement": {
                        "tier": "rtz",
                        "clusters": ["rtz-A", "rtz-B"],
                        "roles": {"rtz-A": "active", "rtz-B": "passive"},
                    },
                },
                "singleton": {
                    "placement": {
                        "tier": "rtz",
                        "clusters": ["rtz-A"],
                        "roles": {"rtz-A": "singleton"},
                    },
                },
            },
        },
        "endpoints": [
            {
                "name": "ui" if protocol == "https" else "wire",
                "hostnameTemplate": hostname,
                "port": port,
                "protocol": protocol,
                "tls": protocol == "https",
                "visibility": "public",
                "launchDefault": True,
                "ssoEnabled": sso_enabled,
            }
        ],
    }


def _active_active_stateless(name: str, hostname: str = None, port: int = 443,
                             protocol: str = "https", sso_enabled: bool = False) -> dict:
    """Stateless Active-Active across rtz-A + rtz-B."""
    if hostname is None:
        hostname = f"{name}.{{{{.OrgSlug}}}}.{{{{.SovereignFQDN}}}}"
    return {
        "topology": {
            "supported": ["active-active", "singleton"],
            "defaults": {
                "multi-region": "active-active",
                "single-region": "singleton",
            },
            "perTopology": {
                "active-active": {
                    "replication": {
                        "backend": "none",
                        "mode": "none",
                    },
                    "switchover": {
                        "mechanism": "none",
                    },
                    "placement": {
                        "tier": "rtz",
                        "clusters": ["rtz-A", "rtz-B"],
                        "roles": {"rtz-A": "active", "rtz-B": "active"},
                    },
                },
                "singleton": {
                    "placement": {
                        "tier": "rtz",
                        "clusters": ["rtz-A"],
                        "roles": {"rtz-A": "singleton"},
                    },
                },
            },
        },
        "endpoints": [
            {
                "name": "ui" if protocol == "https" else "wire",
                "hostnameTemplate": hostname,
                "port": port,
                "protocol": protocol,
                "tls": protocol == "https",
                "visibility": "public",
                "launchDefault": True,
                "ssoEnabled": sso_enabled,
            }
        ],
    }


def _per_org_sso(silent: bool = True, mapping: dict | None = None) -> dict:
    """Per-Org realm SSO with default group→role mapping."""
    if mapping is None:
        mapping = {"sovereign-admins": "admin"}
    return {
        "realm": "{{.OrgSlug}}",
        "protocolMapper": "oidc",
        "groupsClaim": "groups",
        "rolesFromGroup": mapping,
        "silentLogin": silent,
    }


def _multi_instance(enabled: bool = True, max_per_org: int = 10,
                    isolation: str = "namespace") -> dict:
    return {
        "enabled": enabled,
        "maxPerOrg": max_per_org,
        "namingTemplate": "{{.AppName}}-{{.InstanceID}}",
        "isolationLevel": isolation,
    }


def _no_endpoint() -> list:
    """For internal-only Blueprints (no external endpoint per audit)."""
    return []


# ─────────────────────────────────────────────────────────────────────────────
# Per-blueprint declarations (29 App-tier Blueprints in audit order)
# ─────────────────────────────────────────────────────────────────────────────
DECLS: dict[str, dict] = {}


# bp-ferretdb — A/P cnpg sync; Mongo wire; SSO via app-level (no Mongo SSO standard)
DECLS["bp-ferretdb"] = {
    **_active_passive_cnpg_sync(
        "db",
        hostname="db.{{.OrgSlug}}.{{.SovereignFQDN}}",
        port=27017,
        protocol="tcp",
        sso_enabled=False,
    ),
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=10),
}

# bp-valkey — A/P (current valkey/blueprint.yaml says active-hotstandby on multi-region),
# but per audit it's sentinel-based; brief says stateless A/A list excludes valkey →
# active-passive with sentinel.
DECLS["bp-valkey"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "sentinel", "mode": "async"},
                "switchover": {"mechanism": "sentinel-failover", "rtoSeconds": 30},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "wire",
            "hostnameTemplate": "valkey.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 6379,
            "protocol": "tcp",
            "tls": False,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=10),
}

# bp-strimzi — Active-Active with MM2
DECLS["bp-strimzi"] = {
    "topology": {
        "supported": ["active-active", "active-passive", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "mirrormaker2", "mode": "async"},
                "switchover": {"mechanism": "mm2-symmetric"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "active-passive": {
                "replication": {"backend": "mirrormaker2", "mode": "async"},
                "switchover": {"mechanism": "mm2-symmetric"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "wire",
            "hostnameTemplate": "kafka.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 9093,
            "protocol": "tcp",
            "tls": True,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-clickhouse — Active-Active via native replication
DECLS["bp-clickhouse"] = {
    "topology": {
        "supported": ["active-active", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "none", "mode": "async"},
                "switchover": {"mechanism": "none"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "http",
            "hostnameTemplate": "clickhouse.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 8443,
            "protocol": "https",
            "tls": True,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-opensearch — Active-Active via CCR
DECLS["bp-opensearch"] = {
    "topology": {
        "supported": ["active-active", "active-passive", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "ccr", "mode": "async"},
                "switchover": {"mechanism": "ccr-promote"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "active-passive": {
                "replication": {"backend": "ccr", "mode": "async"},
                "switchover": {"mechanism": "ccr-promote"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "api",
            "hostnameTemplate": "opensearch.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": False,
            "ssoEnabled": True,
        },
        {
            "name": "dashboards",
            "hostnameTemplate": "dashboards.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": True,
            "ssoEnabled": True,
        },
    ],
    "sso": _per_org_sso(silent=True, mapping={
        "sovereign-admins": "admin",
        "opensearch-admins": "admin",
        "opensearch-users": "user",
    }),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-stalwart-tenant — A/P cnpg sync + mail blobs on S3 (replicated)
DECLS["bp-stalwart-tenant"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "cnpg-pair", "mode": "sync"},
                "switchover": {"mechanism": "bp-continuum", "rtoSeconds": 60, "rpoSeconds": 0},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "webmail",
            "hostnameTemplate": "mail.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": True,
            "ssoEnabled": True,
        },
        {
            "name": "smtp",
            "hostnameTemplate": "mail.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 587,
            "protocol": "tcp",
            "tls": True,
            "visibility": "public",
            "launchDefault": False,
            "ssoEnabled": False,
        },
        {
            "name": "imap",
            "hostnameTemplate": "mail.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 993,
            "protocol": "tcp",
            "tls": True,
            "visibility": "public",
            "launchDefault": False,
            "ssoEnabled": False,
        },
    ],
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(enabled=False, max_per_org=1),
}

# bp-stalwart-sovereign — external (mothership-hosted)
# Schema enum doesn't have 'external' so we mark it singleton + empty placement
# + ssoEnabled=false + add a comment block in the file noting external status.
DECLS["bp-stalwart-sovereign"] = {
    "topology": {
        "supported": ["singleton"],
        "defaults": {"multi-region": "singleton", "single-region": "singleton"},
        "perTopology": {
            "singleton": {
                "placement": {
                    "tier": "",
                    "clusters": [],
                    "roles": {},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "smtp-external",
            "hostnameTemplate": "mail.openova.io",
            "port": 587,
            "protocol": "tcp",
            "tls": True,
            "visibility": "internal",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(enabled=False, max_per_org=1),
}

# bp-livekit — A/A stateless SFU
DECLS["bp-livekit"] = {
    **_active_active_stateless(
        "livekit",
        hostname="livekit.{{.OrgSlug}}.{{.SovereignFQDN}}",
        port=443,
        protocol="https",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-matrix — A/P cnpg sync + media on S3
DECLS["bp-matrix"] = {
    **_active_passive_cnpg_sync(
        "matrix",
        hostname="matrix.{{.OrgSlug}}.{{.SovereignFQDN}}",
        port=443,
        protocol="https",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-stunner — Active-Active stateless TURN/STUN; no HTTP endpoint
DECLS["bp-stunner"] = {
    "topology": {
        "supported": ["active-active", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "none", "mode": "none"},
                "switchover": {"mechanism": "none"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "turn",
            "hostnameTemplate": "turn.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 3478,
            "protocol": "udp",
            "tls": False,
            "visibility": "public",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(enabled=False, max_per_org=1),
}

# bp-milvus — A/P (vector index on S3 + metadata in etcd)
DECLS["bp-milvus"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "s3-bucket-replication", "mode": "async"},
                "switchover": {"mechanism": "bp-continuum"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "grpc",
            "hostnameTemplate": "milvus.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 19530,
            "protocol": "grpc",
            "tls": True,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-neo4j — A/P (Velero backup-restore as primary cross-region mechanism)
DECLS["bp-neo4j"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "velero", "mode": "async"},
                "switchover": {"mechanism": "bp-velero-restore"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "bolt",
            "hostnameTemplate": "neo4j.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 7687,
            "protocol": "tcp",
            "tls": True,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        },
        {
            "name": "browser",
            "hostnameTemplate": "neo4j.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": True,
            "ssoEnabled": True,
        },
    ],
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-vllm — A/A stateless inference
DECLS["bp-vllm"] = {
    **_active_active_stateless("vllm", sso_enabled=True),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=10),
}

# bp-kserve — A/A stateless model serving
DECLS["bp-kserve"] = {
    **_active_active_stateless("kserve", sso_enabled=True),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-knative — A/A controller; no external endpoint
DECLS["bp-knative"] = {
    "topology": {
        "supported": ["active-active", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "none", "mode": "none"},
                "switchover": {"mechanism": "none"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [],
    "sso": None,
    "multiInstance": _multi_instance(enabled=False, max_per_org=1),
}

# bp-librechat — A/A stateless UI; chat history in shared CNPG
DECLS["bp-librechat"] = {
    **_active_active_stateless(
        "chat",
        hostname="chat.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-bge — A/A stateless embedding
DECLS["bp-bge"] = {
    **_active_active_stateless("bge", sso_enabled=False),
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-llm-gateway — A/A stateless proxy
DECLS["bp-llm-gateway"] = {
    **_active_active_stateless(
        "llm",
        hostname="llm.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-anthropic-adapter — A/A stateless adapter
DECLS["bp-anthropic-adapter"] = {
    **_active_active_stateless("anthropic", sso_enabled=False),
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-langfuse — A/P CNPG sync
DECLS["bp-langfuse"] = {
    **_active_passive_cnpg_sync(
        "langfuse",
        hostname="langfuse.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-nemo-guardrails — A/A stateless policy enforcement; no external endpoint
DECLS["bp-nemo-guardrails"] = {
    "topology": {
        "supported": ["active-active", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "flux-git", "mode": "async"},
                "switchover": {"mechanism": "none"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-temporal — A/P CNPG sync (visibility via opensearch CCR)
DECLS["bp-temporal"] = {
    **_active_passive_cnpg_sync(
        "temporal",
        hostname="temporal.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-flink — A/P checkpoints on S3 (cross-region replicated)
DECLS["bp-flink"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "s3-bucket-replication", "mode": "async"},
                "switchover": {"mechanism": "bp-continuum"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "ui",
            "hostnameTemplate": "flink.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": True,
            "ssoEnabled": True,
        }
    ],
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-debezium — A/P (offsets in bp-strimzi MM2-replicated)
DECLS["bp-debezium"] = {
    "topology": {
        "supported": ["active-passive", "singleton"],
        "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
        "perTopology": {
            "active-passive": {
                "replication": {"backend": "mirrormaker2", "mode": "async"},
                "switchover": {"mechanism": "bp-continuum"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "passive"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=5),
}

# bp-iceberg — A/A on S3
DECLS["bp-iceberg"] = {
    "topology": {
        "supported": ["active-active", "singleton"],
        "defaults": {"multi-region": "active-active", "single-region": "singleton"},
        "perTopology": {
            "active-active": {
                "replication": {"backend": "s3-bucket-replication", "mode": "async"},
                "switchover": {"mechanism": "none"},
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "active", "rtz-B": "active"},
                },
            },
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A"],
                    "roles": {"rtz-A": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "catalog",
            "hostnameTemplate": "iceberg.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "private",
            "launchDefault": False,
            "ssoEnabled": False,
        }
    ],
    "sso": None,
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-openmeter — A/P CNPG sync
DECLS["bp-openmeter"] = {
    **_active_passive_cnpg_sync(
        "openmeter",
        hostname="openmeter.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-litmus — Singleton independent per cluster (chaos exp's are cluster-scoped)
DECLS["bp-litmus"] = {
    "topology": {
        "supported": ["singleton"],
        "defaults": {"multi-region": "singleton", "single-region": "singleton"},
        "perTopology": {
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "singleton", "rtz-B": "singleton"},
                },
            },
        },
    },
    "endpoints": [
        {
            "name": "ui",
            "hostnameTemplate": "litmus.{{.OrgSlug}}.{{.SovereignFQDN}}",
            "port": 443,
            "protocol": "https",
            "tls": True,
            "visibility": "public",
            "launchDefault": True,
            "ssoEnabled": True,
        }
    ],
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(max_per_org=3),
}

# bp-wordpress-tenant — A/P CNPG sync + media on S3 replicated
DECLS["bp-wordpress-tenant"] = {
    **_active_passive_cnpg_sync(
        "wordpress",
        hostname="{{.AppName}}.{{.OrgSlug}}.{{.SovereignFQDN}}",
        sso_enabled=True,
    ),
    "sso": _per_org_sso(silent=True),
    "multiInstance": _multi_instance(enabled=True, max_per_org=20),
}

# bp-qa-app — Test scaffold, singleton, no external endpoint
DECLS["bp-qa-app"] = {
    "topology": {
        "supported": ["singleton"],
        "defaults": {"multi-region": "singleton", "single-region": "singleton"},
        "perTopology": {
            "singleton": {
                "placement": {
                    "tier": "rtz",
                    "clusters": ["rtz-A", "rtz-B"],
                    "roles": {"rtz-A": "singleton", "rtz-B": "singleton"},
                },
            },
        },
    },
    "endpoints": [],
    "sso": None,
    "multiInstance": _multi_instance(enabled=False, max_per_org=1),
}


def all_decls() -> dict[str, dict]:
    return DECLS


if __name__ == "__main__":
    print(f"declared {len(DECLS)} App-tier Blueprints")
    for k in sorted(DECLS):
        print(f"  {k}")
