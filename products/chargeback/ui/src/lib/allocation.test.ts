import { describe, expect, it } from 'vitest'
import { basis, basisPreview, sumHours } from './allocation'

describe('allocation basis', () => {
  const w = { vcpu: 1, mem_gib: 0.5, pvc_gb: 0.1 }
  const h = { vcpu_hours: 100, mem_gib_hours: 200, pvc_gb_hours: 1000 }
  it('is the weighted sum of the three consumption counters', () => {
    expect(basis(w, h)).toBe(100 * 1 + 200 * 0.5 + 1000 * 0.1)
  })
  // Each weight must act on its own counter — swapping two weights must
  // change the result, or the inputs are wired to the wrong terms.
  it('applies each weight to its own counter', () => {
    expect(basis({ vcpu: 0.5, mem_gib: 1, pvc_gb: 0.1 }, h)).not.toBe(basis(w, h))
    expect(basis({ vcpu: 1, mem_gib: 0, pvc_gb: 0 }, h)).toBe(100)
    expect(basis({ vcpu: 0, mem_gib: 1, pvc_gb: 0 }, h)).toBe(200)
    expect(basis({ vcpu: 0, mem_gib: 0, pvc_gb: 1 }, h)).toBe(1000)
  })
  it('is zero when every weight is zero', () => {
    expect(basis({ vcpu: 0, mem_gib: 0, pvc_gb: 0 }, h)).toBe(0)
  })
  it('previews term by term in the order of the inputs', () => {
    const p = basisPreview(w, h)
    expect(p.terms.map((t) => [t.label, t.product])).toEqual([
      ['vCPU-h', 100],
      ['GiB-h', 100],
      ['GB-h', 100],
    ])
    expect(p.total).toBe(300)
  })
  it('sums hours across rows and treats no rows as zero consumption', () => {
    expect(sumHours([h, { vcpu_hours: 1, mem_gib_hours: 2, pvc_gb_hours: 3 }])).toEqual({ vcpu_hours: 101, mem_gib_hours: 202, pvc_gb_hours: 1003 })
    expect(sumHours([])).toEqual({ vcpu_hours: 0, mem_gib_hours: 0, pvc_gb_hours: 0 })
  })
})
