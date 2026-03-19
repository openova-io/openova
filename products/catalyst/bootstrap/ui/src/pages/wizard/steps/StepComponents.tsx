import { useState, useMemo } from 'react'
import { Search, Lock, ChevronDown, ChevronUp } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { PLATFORM_COMPONENTS, COMPONENT_CATEGORIES, type ComponentCategory } from '@/shared/constants/components'
import type { SelectedComponent } from '@/entities/deployment/model'
import { Input } from '@/shared/ui/input'
import { Badge } from '@/shared/ui/badge'
import { Checkbox } from '@/shared/ui/checkbox'
import { Separator } from '@/shared/ui/separator'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/shared/ui/tooltip'
import { cn } from '@/shared/lib/utils'
import { StepShell, useStepNav } from './_shared'

const MANDATORY = PLATFORM_COMPONENTS.filter((c) => c.required)
const OPTIONAL = PLATFORM_COMPONENTS.filter((c) => !c.required)

export function StepComponents() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const [search, setSearch] = useState('')
  const [expandedCategories, setExpandedCategories] = useState<Set<ComponentCategory>>(
    new Set(OPTIONAL.map((c) => c.category))
  )

  const selectedIds = useMemo(
    () => new Set(store.selectedComponents.map((c) => c.id)),
    [store.selectedComponents]
  )

  function toggleCategory(cat: ComponentCategory) {
    setExpandedCategories((prev) => {
      const next = new Set(prev)
      if (next.has(cat)) next.delete(cat)
      else next.add(cat)
      return next
    })
  }

  function handleToggle(comp: typeof PLATFORM_COMPONENTS[number]) {
    if (comp.required) return
    const selected: SelectedComponent = {
      id: comp.id,
      name: comp.name,
      version: comp.version,
      category: comp.category,
      required: comp.required,
      dependencies: comp.dependencies,
    }
    store.toggleComponent(selected)
  }

  function isBlocked(comp: typeof PLATFORM_COMPONENTS[number]) {
    return comp.dependencies.some(
      (dep) => !MANDATORY.find((m) => m.id === dep) && !selectedIds.has(dep)
    )
  }

  const filteredOptional = OPTIONAL.filter(
    (c) => !search || c.name.toLowerCase().includes(search.toLowerCase()) || c.description.toLowerCase().includes(search.toLowerCase())
  )

  const byCategory = useMemo(() => {
    const map = new Map<ComponentCategory, typeof PLATFORM_COMPONENTS>()
    for (const comp of filteredOptional) {
      if (!map.has(comp.category)) map.set(comp.category, [])
      map.get(comp.category)!.push(comp)
    }
    return map
  }, [filteredOptional])

  const totalSelected = MANDATORY.length + store.selectedComponents.length

  return (
    <StepShell
      title="Select platform components"
      description="Core components are always installed. Choose optional components for your use case. Dependencies are enforced automatically."
      onNext={next}
      onBack={back}
      nextLabel={`Continue with ${totalSelected} component${totalSelected !== 1 ? 's' : ''}`}
    >
      {/* Mandatory */}
      <div className="flex flex-col gap-2">
        <p className="text-xs font-semibold text-[oklch(45%_0.01_250)] uppercase tracking-wider">
          Core — always installed ({MANDATORY.length})
        </p>
        <div className="rounded-[--radius-lg] border border-[--color-surface-border] divide-y divide-[--color-surface-border] overflow-hidden">
          {MANDATORY.map((comp) => (
            <div key={comp.id} className="flex items-center gap-3 px-4 py-3 bg-[--color-surface-1]">
              <Lock className="h-3.5 w-3.5 shrink-0 text-[oklch(40%_0.01_250)]" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm font-semibold text-[oklch(80%_0.01_250)]">{comp.name}</span>
                  <Badge variant="default" className="text-[10px]">{comp.version}</Badge>
                  <Badge variant="brand" className="text-[10px]">{COMPONENT_CATEGORIES[comp.category].label}</Badge>
                </div>
                <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">{comp.description}</p>
              </div>
            </div>
          ))}
        </div>
      </div>

      <Separator />

      {/* Optional search */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <p className="text-xs font-semibold text-[oklch(45%_0.01_250)] uppercase tracking-wider">
            Optional — {store.selectedComponents.length} selected
          </p>
          <Input
            type="search"
            placeholder="Search components..."
            prefix={<Search className="h-3.5 w-3.5" />}
            className="w-44 h-7 text-xs"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>

        {/* Grouped by category */}
        {Array.from(byCategory.entries()).map(([category, comps]) => {
          const expanded = expandedCategories.has(category)
          const catConfig = COMPONENT_CATEGORIES[category]
          const selectedInCat = comps.filter((c) => selectedIds.has(c.id)).length

          return (
            <div key={category} className="rounded-[--radius-lg] border border-[--color-surface-border] overflow-hidden">
              <button
                onClick={() => toggleCategory(category)}
                className="w-full flex items-center gap-3 px-4 py-3 bg-[--color-surface-1] hover:bg-[--color-surface-2] transition-colors"
              >
                <span className={cn('text-xs font-semibold uppercase tracking-wider', catConfig.color)}>
                  {catConfig.label}
                </span>
                <span className="text-xs text-[oklch(40%_0.01_250)]">
                  {comps.length} component{comps.length !== 1 ? 's' : ''}
                  {selectedInCat > 0 && ` · ${selectedInCat} selected`}
                </span>
                <div className="ml-auto text-[oklch(45%_0.01_250)]">
                  {expanded ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
                </div>
              </button>

              {expanded && (
                <div className="divide-y divide-[--color-surface-border]">
                  {comps.map((comp) => {
                    const checked = selectedIds.has(comp.id)
                    const blocked = isBlocked(comp)

                    const row = (
                      <div
                        key={comp.id}
                        className={cn(
                          'flex items-start gap-3 px-4 py-3 bg-[--color-surface-0] transition-colors',
                          !blocked && 'hover:bg-[--color-surface-1] cursor-pointer',
                          blocked && 'opacity-50 cursor-not-allowed'
                        )}
                        onClick={() => !blocked && handleToggle(comp)}
                      >
                        <Checkbox
                          checked={checked}
                          disabled={blocked}
                          className="mt-0.5"
                          onCheckedChange={() => !blocked && handleToggle(comp)}
                        />
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2 flex-wrap">
                            <span className="text-sm font-semibold text-[oklch(80%_0.01_250)]">{comp.name}</span>
                            <Badge variant="default" className="text-[10px]">{comp.version}</Badge>
                            {comp.dependencies.length > 0 && (
                              <span className="text-[10px] text-[oklch(40%_0.01_250)]">
                                needs: {comp.dependencies.join(', ')}
                              </span>
                            )}
                          </div>
                          <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">{comp.description}</p>
                        </div>
                      </div>
                    )

                    if (blocked) {
                      const missingDeps = comp.dependencies.filter(
                        (d) => !MANDATORY.find((m) => m.id === d) && !selectedIds.has(d)
                      )
                      return (
                        <Tooltip key={comp.id}>
                          <TooltipTrigger asChild>{row}</TooltipTrigger>
                          <TooltipContent>
                            Requires: {missingDeps.join(', ')}. Enable the dependency first.
                          </TooltipContent>
                        </Tooltip>
                      )
                    }

                    return row
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </StepShell>
  )
}
