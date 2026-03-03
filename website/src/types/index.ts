export interface Component {
  name: string;
  slug: string;
  purpose: string;
  category: ComponentCategory;
  type: 'core' | 'alacarte';
}

export type ComponentCategory =
  | 'infrastructure'
  | 'networking'
  | 'gitops'
  | 'security'
  | 'supply-chain'
  | 'policy'
  | 'scaling'
  | 'operations'
  | 'observability'
  | 'registry'
  | 'storage'
  | 'failover'
  | 'data'
  | 'communication'
  | 'workflow'
  | 'analytics'
  | 'ai-ml'
  | 'ai-safety'
  | 'identity';

export interface Product {
  name: string;
  fullName: string;
  description: string;
  color: string;
  icon: string;
  components: string[];
}

export interface NavItem {
  label: string;
  href: string;
}

export interface MigrationPath {
  from: string;
  to: string;
  savings?: string;
  challenges?: string;
  category: 'platform' | 'database' | 'observability' | 'security' | 'cicd';
}

export type PlatformLayer =
  | 'networking'
  | 'security'
  | 'gitops'
  | 'observability'
  | 'storage'
  | 'scaling'
  | 'data'
  | 'ai'
  | 'communication'
  | 'identity';

export interface LayerDefinition {
  id: PlatformLayer;
  label: string;
  color: string;
  type: 'core' | 'alacarte';
  categories: ComponentCategory[];
}
