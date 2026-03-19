import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Link } from '@tanstack/react-router'
import { motion } from 'framer-motion'
import { ArrowLeft, Send } from 'lucide-react'
import { useState } from 'react'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'

const schema = z.object({ email: z.string().email('Enter a valid email address') })
type FormValues = z.infer<typeof schema>

export function ForgotPage() {
  const [sent, setSent] = useState(false)
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<FormValues>({ resolver: zodResolver(schema) })

  async function onSubmit(_data: FormValues) {
    await new Promise((r) => setTimeout(r, 600))
    setSent(true)
  }

  return (
    <AuthShell>
      <motion.div initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.4 }} className="flex flex-col gap-8">
        {!sent ? (
          <>
            <div>
              <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Reset password</h1>
              <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">Enter your email and we'll send you a reset link.</p>
            </div>
            <form onSubmit={handleSubmit(onSubmit)} className="flex flex-col gap-4" noValidate>
              <Input label="Email" type="email" placeholder="you@company.com" autoFocus error={errors.email?.message} {...register('email')} />
              <Button type="submit" loading={isSubmitting} size="lg" className="w-full">
                <Send className="h-4 w-4" />
                Send reset link
              </Button>
            </form>
          </>
        ) : (
          <motion.div initial={{ opacity: 0, scale: 0.96 }} animate={{ opacity: 1, scale: 1 }} className="flex flex-col items-center gap-4 text-center py-6">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[--color-success]/15">
              <Send className="h-5 w-5 text-[--color-success]" />
            </div>
            <div>
              <p className="font-semibold text-[oklch(92%_0.01_250)]">Check your inbox</p>
              <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">If an account exists, we've sent a reset link.</p>
            </div>
          </motion.div>
        )}
        <Link to="/login" className="flex items-center gap-1.5 text-sm text-[oklch(50%_0.01_250)] hover:text-[oklch(75%_0.01_250)] transition-colors">
          <ArrowLeft className="h-4 w-4" />
          Back to sign in
        </Link>
      </motion.div>
    </AuthShell>
  )
}
