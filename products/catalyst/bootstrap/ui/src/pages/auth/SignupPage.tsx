import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Link } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { ArrowRight, Eye, EyeOff } from 'lucide-react'
import { useState } from 'react'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'

const schema = z.object({
  name: z.string().min(2, 'Enter your full name'),
  email: z.string().email('Enter a valid email address'),
  password: z
    .string()
    .min(12, 'Password must be at least 12 characters')
    .regex(/[A-Z]/, 'Must contain an uppercase letter')
    .regex(/[0-9]/, 'Must contain a number'),
  organisation: z.string().min(2, 'Enter your organisation name'),
})
type FormValues = z.infer<typeof schema>

export function SignupPage() {
  const [showPassword, setShowPassword] = useState(false)
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema) })

  async function onSubmit(_data: FormValues) {
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
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Create your account</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
            Start provisioning production-grade Kubernetes in minutes.
          </p>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="flex flex-col gap-4" noValidate>
          <Input label="Full name" type="text" placeholder="Emrah Baysal" autoComplete="name" autoFocus error={errors.name?.message} {...register('name')} />
          <Input label="Work email" type="email" placeholder="you@company.com" autoComplete="email" error={errors.email?.message} {...register('email')} />
          <Input label="Organisation" type="text" placeholder="Acme Corp" autoComplete="organization" error={errors.organisation?.message} {...register('organisation')} />
          <Input
            label="Password"
            type={showPassword ? 'text' : 'password'}
            placeholder="12+ characters"
            autoComplete="new-password"
            error={errors.password?.message}
            hint="Minimum 12 characters, one uppercase, one number"
            suffix={
              <button type="button" onClick={() => setShowPassword((v) => !v)} className="text-[oklch(50%_0.01_250)] hover:text-[oklch(75%_0.01_250)] transition-colors" aria-label={showPassword ? 'Hide' : 'Show'}>
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            }
            {...register('password')}
          />

          <Button type="submit" loading={isSubmitting} size="lg" className="mt-1 w-full">
            Create account
            <ArrowRight className="h-4 w-4" />
          </Button>
        </form>

        <p className="text-center text-sm text-[oklch(50%_0.01_250)]">
          Already have an account?{' '}
          <Link to="/login" className="text-[--color-brand-400] hover:text-[--color-brand-300] font-medium transition-colors">
            Sign in
          </Link>
        </p>

        <p className="text-center text-xs text-[oklch(35%_0.01_250)]">
          By creating an account you agree to our Terms of Service and Privacy Policy.
        </p>
      </motion.div>
    </AuthShell>
  )
}
