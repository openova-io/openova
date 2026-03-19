import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Link } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { Eye, EyeOff, ArrowRight } from 'lucide-react'
import { useState } from 'react'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import { Separator } from '@/shared/ui/separator'

const schema = z.object({
  email: z.string().email('Enter a valid email address'),
  password: z.string().min(8, 'Password must be at least 8 characters'),
})
type FormValues = z.infer<typeof schema>

export function LoginPage() {
  const [showPassword, setShowPassword] = useState(false)
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })

  async function onSubmit(_data: FormValues) {
    // TODO: wire to auth API
    await new Promise((r) => setTimeout(r, 800))
  }

  return (
    <AuthShell>
      <motion.div
        initial={{ opacity: 0, y: 16 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.4, ease: [0.4, 0, 0.2, 1] }}
        className="flex flex-col gap-8"
      >
        <div>
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Welcome back</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">Sign in to your Catalyst account</p>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="flex flex-col gap-4" noValidate>
          <Input
            label="Email"
            type="email"
            placeholder="you@company.com"
            autoComplete="email"
            autoFocus
            error={errors.email?.message}
            {...register('email')}
          />
          <Input
            label="Password"
            type={showPassword ? 'text' : 'password'}
            placeholder="••••••••"
            autoComplete="current-password"
            error={errors.password?.message}
            suffix={
              <button
                type="button"
                onClick={() => setShowPassword((v) => !v)}
                className="text-[oklch(50%_0.01_250)] hover:text-[oklch(75%_0.01_250)] transition-colors"
                aria-label={showPassword ? 'Hide password' : 'Show password'}
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            }
            {...register('password')}
          />

          <div className="flex justify-end">
            <Link to="/forgot" className="text-xs text-[--color-brand-400] hover:text-[--color-brand-300] transition-colors">
              Forgot password?
            </Link>
          </div>

          <Button type="submit" loading={isSubmitting} size="lg" className="mt-1 w-full">
            Sign in
            <ArrowRight className="h-4 w-4" />
          </Button>
        </form>

        <div className="flex items-center gap-3">
          <Separator className="flex-1" />
          <span className="text-xs text-[oklch(40%_0.01_250)]">or</span>
          <Separator className="flex-1" />
        </div>

        <p className="text-center text-sm text-[oklch(50%_0.01_250)]">
          No account?{' '}
          <Link to="/signup" className="text-[--color-brand-400] hover:text-[--color-brand-300] font-medium transition-colors">
            Create one
          </Link>
        </p>
      </motion.div>
    </AuthShell>
  )
}
