import * as ProgressPrimitive from '@radix-ui/react-progress'
import { cn } from '@/shared/lib/utils'

interface ProgressProps extends React.ComponentProps<typeof ProgressPrimitive.Root> {
  indicatorClassName?: string
}

function Progress({ className, value, indicatorClassName, ...props }: ProgressProps) {
  return (
    <ProgressPrimitive.Root
      className={cn(
        'relative h-1.5 w-full overflow-hidden rounded-full bg-[--color-surface-border]',
        className
      )}
      {...props}
    >
      <ProgressPrimitive.Indicator
        className={cn(
          'h-full bg-[--color-brand-500] transition-all duration-500 ease-[--ease-smooth]',
          indicatorClassName
        )}
        style={{ transform: `translateX(-${100 - (value ?? 0)}%)` }}
      />
    </ProgressPrimitive.Root>
  )
}

export { Progress }
