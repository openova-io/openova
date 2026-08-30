// Invite-activation form validation (spec §4 POST /invites/{token}/activate:
// {region, project_ids[], access_key, secret_key}). Pure — tested in
// activation.test.ts. The secret key is validated for presence/shape only
// and is never echoed anywhere in the UI.

export interface ActivationInput {
  region: string
  projectIds: string
  accessKey: string
  secretKey: string
}

export interface ActivationErrors {
  region?: string
  projectIds?: string
  accessKey?: string
  secretKey?: string
}

const REGION_RE = /^[a-z0-9][a-z0-9-]{1,62}$/
const PROJECT_RE = /^[A-Za-z0-9_-]{8,64}$/

/** project ids may be separated by newline, comma, semicolon or whitespace. */
export function parseProjectIds(raw: string): string[] {
  const out: string[] = []
  for (const p of raw.split(/[\s,;]+/)) {
    const v = p.trim()
    if (v && !out.includes(v)) out.push(v)
  }
  return out
}

export function validateActivation(input: ActivationInput): ActivationErrors {
  const errors: ActivationErrors = {}
  const region = input.region.trim()
  if (!region) errors.region = 'region is required'
  else if (!REGION_RE.test(region)) errors.region = 'region must be lowercase letters, digits and dashes (e.g. om-east-1)'

  const ids = parseProjectIds(input.projectIds)
  if (ids.length === 0) errors.projectIds = 'at least one project id is required'
  else {
    const bad = ids.filter((id) => !PROJECT_RE.test(id))
    if (bad.length) errors.projectIds = `invalid project id: ${bad[0]}`
  }

  const ak = input.accessKey.trim()
  if (!ak) errors.accessKey = 'access key is required'
  else if (/\s/.test(ak) || ak.length < 10 || ak.length > 64) errors.accessKey = 'access key must be 10-64 characters without spaces'

  const sk = input.secretKey
  if (!sk) errors.secretKey = 'secret key is required'
  else if (sk.trim() !== sk) errors.secretKey = 'secret key has leading or trailing whitespace'
  else if (sk.length < 16 || sk.length > 128) errors.secretKey = 'secret key must be 16-128 characters'

  return errors
}

export function hasErrors(e: ActivationErrors): boolean {
  return Object.keys(e).length > 0
}
