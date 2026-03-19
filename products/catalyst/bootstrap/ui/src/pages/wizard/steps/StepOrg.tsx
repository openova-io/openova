import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useWizardStore } from '@/entities/deployment/store'
import { Input } from '@/shared/ui/input'
import { StepShell, useStepNav } from './_shared'

const schema = z.object({
  orgName: z.string().min(2, 'Minimum 2 characters').max(48, 'Maximum 48 characters'),
  orgDomain: z
    .string()
    .min(4, 'Enter a valid domain')
    .regex(/^([a-z0-9-]+\.)+[a-z]{2,}$/, 'Enter a valid domain (e.g. acme.io)'),
  orgEmail: z.string().email('Enter a valid email address'),
})
type FormValues = z.infer<typeof schema>

export function StepOrg() {
  const store = useWizardStore()
  const { next } = useStepNav()

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      orgName: store.orgName,
      orgDomain: store.orgDomain,
      orgEmail: store.orgEmail,
    },
  })

  function onSubmit(data: FormValues) {
    store.setOrgName(data.orgName)
    store.setOrgDomain(data.orgDomain)
    store.setOrgEmail(data.orgEmail)
    next()
  }

  return (
    <StepShell
      title="Tell us about your organisation"
      description="This information is used to name your clusters and configure platform defaults. It stays in your environment."
      onNext={handleSubmit(onSubmit)}
      onBack={undefined}
    >
      <Input
        label="Organisation name"
        placeholder="Acme Corp"
        autoFocus
        required
        error={errors.orgName?.message}
        hint="Used as the cluster owner identifier"
        {...register('orgName')}
      />
      <Input
        label="Domain"
        placeholder="acme.io"
        required
        error={errors.orgDomain?.message}
        hint="Your primary domain — used for service URLs and TLS certificates"
        {...register('orgDomain')}
      />
      <Input
        label="Technical contact email"
        type="email"
        placeholder="platform@acme.io"
        required
        error={errors.orgEmail?.message}
        hint="Receives cert-manager expiry alerts and critical notifications"
        {...register('orgEmail')}
      />
    </StepShell>
  )
}
