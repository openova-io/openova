import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { CloudProviderSelector } from '@/widgets/cloud-provider-card/CloudProviderCard'
import { StepShell, useStepNav } from './_shared'

export function StepProvider() {
  const { provider, setProvider } = useWizardStore()
  const { next, back } = useStepNav()

  return (
    <StepShell
      title="Choose your cloud provider"
      description="Select the cloud provider where your OpenOva clusters will be provisioned. Additional providers are on the roadmap."
      onNext={next}
      onBack={back}
      nextDisabled={!provider}
    >
      <CloudProviderSelector
        value={provider}
        onChange={(p: CloudProvider) => setProvider(p)}
      />
    </StepShell>
  )
}
