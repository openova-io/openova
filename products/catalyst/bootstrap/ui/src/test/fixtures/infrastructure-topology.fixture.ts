/**
 * infrastructure-topology.fixture.ts — synthetic hierarchical
 * infrastructure tree for tests + the local-dev fallback when the
 * /infrastructure/topology backend isn't deployed.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall) — the FINAL shape
 * is locked in here so every consumer (tests, dev fallback,
 * Storybook) speaks the same vocabulary.
 */

import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'

export const infrastructureTopologyFixture: HierarchicalInfrastructure = {
  cloud: [
    {
      id: 'cloud-hetzner',
      name: 'Hetzner Cloud',
      provider: 'hetzner',
      regionCount: 2,
      quotaUsed: 12,
      quotaLimit: 50,
    },
  ],
  topology: {
    pattern: 'ha-pair',
    regions: [
      {
        id: 'region-eu-central',
        name: 'Frankfurt',
        provider: 'hetzner',
        providerRegion: 'fsn1',
        skuCp: 'cpx32',
        skuWorker: 'cpx32',
        workerCount: 3,
        status: 'healthy',
        clusters: [
          {
            id: 'cluster-eu-central-primary',
            name: 'omantel-primary',
            version: 'v1.31.4+k3s1',
            status: 'healthy',
            nodeCount: 4,
            vclusters: [
              {
                id: 'vc-eu-central-dmz',
                name: 'dmz',
                isolationMode: 'dmz',
                status: 'healthy',
              },
              {
                id: 'vc-eu-central-rtz',
                name: 'rtz',
                isolationMode: 'rtz',
                status: 'healthy',
              },
              {
                id: 'vc-eu-central-mgmt',
                name: 'mgmt',
                isolationMode: 'mgmt',
                status: 'healthy',
              },
            ],
            loadBalancers: [
              {
                id: 'lb-eu-central-edge',
                name: 'edge-lb',
                publicIP: '116.203.42.1',
                listeners: [
                  { port: 80, protocol: 'tcp' },
                  { port: 443, protocol: 'tcp' },
                ],
                targets: [
                  { id: 'tgt-1', ip: '10.0.1.10', status: 'healthy' },
                  { id: 'tgt-2', ip: '10.0.1.11', status: 'healthy' },
                ],
                region: 'fsn1',
                status: 'healthy',
              },
            ],
            nodePools: [
              {
                id: 'pool-eu-cp',
                sku: 'cpx32',
                replicas: 1,
                status: 'healthy',
              },
              {
                id: 'pool-eu-worker',
                sku: 'cpx32',
                replicas: 3,
                status: 'healthy',
              },
            ],
            nodes: [
              {
                id: 'node-eu-cp-0',
                name: 'eu-cp-0',
                sku: 'cpx32',
                role: 'control-plane',
                ip: '10.0.1.5',
                status: 'healthy',
              },
              {
                id: 'node-eu-w-0',
                name: 'eu-w-0',
                sku: 'cpx32',
                role: 'worker',
                ip: '10.0.1.10',
                status: 'healthy',
              },
              {
                id: 'node-eu-w-1',
                name: 'eu-w-1',
                sku: 'cpx32',
                role: 'worker',
                ip: '10.0.1.11',
                status: 'healthy',
              },
              {
                id: 'node-eu-w-2',
                name: 'eu-w-2',
                sku: 'cpx32',
                role: 'worker',
                ip: '10.0.1.12',
                status: 'degraded',
              },
            ],
          },
        ],
        networks: [
          {
            id: 'net-eu-central',
            cidr: '10.0.0.0/16',
            region: 'fsn1',
            peerings: [
              {
                id: 'peer-eu-to-hel',
                name: 'eu-fsn1↔hel1',
                vpcPair: 'net-eu-central → net-hel1',
                subnets: '10.0.0.0/16,10.1.0.0/16',
                status: 'healthy',
              },
            ],
            firewalls: [
              {
                id: 'fw-eu-central',
                name: 'edge-firewall',
                rules: [
                  {
                    id: 'fw-rule-https',
                    protocol: 'tcp',
                    port: '443',
                    source: '0.0.0.0/0',
                    action: 'allow',
                  },
                ],
                status: 'healthy',
              },
            ],
          },
        ],
      },
      {
        id: 'region-eu-helsinki',
        name: 'Helsinki',
        provider: 'hetzner',
        providerRegion: 'hel1',
        skuCp: 'cpx32',
        skuWorker: 'cpx32',
        workerCount: 1,
        status: 'healthy',
        clusters: [
          {
            id: 'cluster-eu-helsinki-secondary',
            name: 'omantel-secondary',
            version: 'v1.31.4+k3s1',
            status: 'healthy',
            nodeCount: 2,
            vclusters: [
              {
                id: 'vc-hel-rtz',
                name: 'rtz',
                isolationMode: 'rtz',
                status: 'healthy',
              },
            ],
            loadBalancers: [],
            nodePools: [
              {
                id: 'pool-hel-cp',
                sku: 'cpx32',
                replicas: 1,
                status: 'healthy',
              },
            ],
            nodes: [
              {
                id: 'node-hel-cp-0',
                name: 'hel-cp-0',
                sku: 'cpx32',
                role: 'control-plane',
                ip: '10.1.1.5',
                status: 'healthy',
              },
              {
                id: 'node-hel-w-0',
                name: 'hel-w-0',
                sku: 'cpx32',
                role: 'worker',
                ip: '10.1.1.10',
                status: 'healthy',
              },
            ],
          },
        ],
        networks: [
          {
            id: 'net-hel1',
            cidr: '10.1.0.0/16',
            region: 'hel1',
            peerings: [],
            firewalls: [],
          },
        ],
      },
    ],
  },
  storage: {
    pvcs: [
      {
        id: 'pvc-postgres-data',
        name: 'postgres-data',
        namespace: 'gitea',
        capacity: '20Gi',
        used: '4.2Gi',
        storageClass: 'local-path',
        status: 'healthy',
      },
      {
        id: 'pvc-redis-data',
        name: 'redis-data',
        namespace: 'mailcow',
        capacity: '5Gi',
        used: '120Mi',
        storageClass: 'local-path',
        status: 'healthy',
      },
    ],
    buckets: [
      {
        id: 'bucket-backups',
        name: 'backups',
        endpoint: 'seaweedfs.seaweedfs.svc:8333',
        capacity: '100Gi',
        used: '12.5Gi',
        retentionDays: '30',
      },
    ],
    volumes: [
      {
        id: 'vol-postgres-eu',
        name: 'postgres-eu-vol',
        capacity: '50Gi',
        region: 'fsn1',
        attachedTo: 'node-eu-w-0',
        status: 'healthy',
      },
    ],
  },
}
