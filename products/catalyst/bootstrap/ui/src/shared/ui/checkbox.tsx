import * as CheckboxPrimitive from '@radix-ui/react-checkbox'
import { Check } from 'lucide-react'
import { cn } from '@/shared/lib/utils'

function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      className={cn(
        'peer h-4 w-4 shrink-0 rounded-[--radius-sm]',
        'border border-[--color-surface-border] bg-[--color-surface-1]',
        'transition-all duration-150 cursor-pointer',
        'hover:border-[--color-brand-500]/60',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[--color-brand-500]',
        'data-[state=checked]:bg-[--color-brand-500] data-[state=checked]:border-[--color-brand-500]',
        'disabled:cursor-not-allowed disabled:opacity-40',
        className
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator className="flex items-center justify-center text-white">
        <Check className="h-3 w-3" strokeWidth={3} />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}

export { Checkbox }
