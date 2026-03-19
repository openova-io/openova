import { motion } from 'framer-motion'
import { Check, Lock } from 'lucide-react'
import { cn } from '@/shared/lib/utils'
import { Badge } from '@/shared/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/shared/ui/tooltip'
import type { CloudProvider } from '@/entities/deployment/model'

interface CloudProviderOption {
  id: CloudProvider
  name: string
  description: string
  regions: number
  available: boolean
  comingSoon?: boolean
  logo: React.ReactNode
}

interface CloudProviderCardProps {
  provider: CloudProviderOption
  selected: boolean
  onSelect: (id: CloudProvider) => void
}

function HetznerLogo() {
  return (
    <svg viewBox="0 0 48 48" fill="none" className="h-8 w-8">
      <rect width="48" height="48" rx="8" fill="#D50C2D" />
      <path d="M14 12h6v10h8V12h6v24h-6V28h-8v8h-6V12z" fill="white" />
    </svg>
  )
}

function HuaweiLogo() {
  return (
    <svg viewBox="0 0 48 48" fill="none" className="h-8 w-8 opacity-40">
      <rect width="48" height="48" rx="8" fill="#CF0A2C" />
      <path d="M24 8C24 8 14 16 14 24C14 32 24 40 24 40C24 40 34 32 34 24C34 16 24 8 24 8Z" fill="white" fillOpacity="0.6"/>
      <circle cx="24" cy="24" r="4" fill="white"/>
    </svg>
  )
}

function OCILogo() {
  return (
    <svg viewBox="0 0 48 48" fill="none" className="h-8 w-8 opacity-40">
      <rect width="48" height="48" rx="8" fill="#C74634" />
      <text x="8" y="30" fontSize="14" fontWeight="bold" fill="white">OCI</text>
    </svg>
  )
}

const PROVIDERS: CloudProviderOption[] = [
  {
    id: 'hetzner',
    name: 'Hetzner Cloud',
    description: 'European cloud — private networking, hourly billing, official CSI driver',
    regions: 6,
    available: true,
    logo: <HetznerLogo />,
  },
  {
    id: 'huawei',
    name: 'Huawei Cloud',
    description: 'Enterprise cloud for regulated markets — CCE, OBS, VPC peering',
    regions: 4,
    available: false,
    comingSoon: true,
    logo: <HuaweiLogo />,
  },
  {
    id: 'oci',
    name: 'Oracle Cloud',
    description: 'High-performance bare metal — OKE, Block Volumes, FastConnect',
    regions: 5,
    available: false,
    comingSoon: true,
    logo: <OCILogo />,
  },
]

function ProviderCard({ provider, selected, onSelect }: CloudProviderCardProps) {
  const card = (
    <motion.div
      whileHover={provider.available ? { scale: 1.015 } : {}}
      whileTap={provider.available ? { scale: 0.99 } : {}}
      onClick={() => provider.available && onSelect(provider.id)}
      className={cn(
        'relative flex flex-col gap-4 rounded-[--radius-lg] border p-5 transition-all duration-200',
        provider.available ? 'cursor-pointer' : 'cursor-not-allowed',
        selected
          ? 'border-[--color-brand-500]/60 bg-[--color-brand-500]/8 shadow-[0_0_0_1px_var(--color-brand-500)/30]'
          : provider.available
          ? 'border-[--color-surface-border] bg-[--color-surface-1] hover:border-[oklch(28%_0.02_250)] hover:bg-[--color-surface-2]'
          : 'border-[--color-surface-border] bg-[--color-surface-1] opacity-50'
      )}
      role="radio"
      aria-checked={selected}
      tabIndex={provider.available ? 0 : -1}
      onKeyDown={(e) => {
        if ((e.key === 'Enter' || e.key === ' ') && provider.available) {
          e.preventDefault()
          onSelect(provider.id)
        }
      }}
    >
      {/* Selection indicator */}
      {selected && (
        <motion.div
          initial={{ scale: 0 }}
          animate={{ scale: 1 }}
          className="absolute right-4 top-4 flex h-5 w-5 items-center justify-center rounded-full bg-[--color-brand-500]"
        >
          <Check className="h-3 w-3 text-white" strokeWidth={3} />
        </motion.div>
      )}

      {/* Coming soon badge */}
      {provider.comingSoon && (
        <div className="absolute right-4 top-4 flex items-center gap-1 text-[oklch(45%_0.01_250)]">
          <Lock className="h-3 w-3" />
          <span className="text-xs">Coming soon</span>
        </div>
      )}

      <div className="flex items-center gap-3">
        {provider.logo}
        <div>
          <p className="text-sm font-semibold text-[oklch(92%_0.01_250)]">{provider.name}</p>
          <p className="text-xs text-[oklch(50%_0.01_250)] mt-0.5">
            {provider.regions} region{provider.regions !== 1 ? 's' : ''}
          </p>
        </div>
      </div>

      <p className="text-xs text-[oklch(55%_0.01_250)] leading-relaxed">
        {provider.description}
      </p>

      {provider.id === 'hetzner' && (
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="success">Hourly billing</Badge>
          <Badge variant="info">Private networking</Badge>
          <Badge variant="default">CSI driver</Badge>
        </div>
      )}
    </motion.div>
  )

  if (!provider.available) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>{card}</TooltipTrigger>
        <TooltipContent>
          {provider.name} support is on the roadmap. Hetzner is available now.
        </TooltipContent>
      </Tooltip>
    )
  }

  return card
}

interface CloudProviderSelectorProps {
  value: CloudProvider | null
  onChange: (provider: CloudProvider) => void
}

export function CloudProviderSelector({ value, onChange }: CloudProviderSelectorProps) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Cloud provider">
      {PROVIDERS.map((provider) => (
        <ProviderCard
          key={provider.id}
          provider={provider}
          selected={value === provider.id}
          onSelect={onChange}
        />
      ))}
    </div>
  )
}
