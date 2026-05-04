import { describe, it, expect } from 'vitest'
import {
  COMPONENT_FOOTPRINTS,
  CONTROL_PLANE_OVERHEAD,
  footprintFor,
  recommendedWorkers,
  sumFootprints,
  workerCapacity,
} from './componentFootprints'

describe('componentFootprints', () => {
  describe('footprintFor', () => {
    it('returns catalog entry for a known component', () => {
      const f = footprintFor('cilium')
      expect(f.ramMb).toBeGreaterThan(0)
      expect(f.cpuMilli).toBeGreaterThan(0)
    })

    it('returns zero footprint for an unknown component (sentinel)', () => {
      // Phase-0 fixtures (opentofu, vcluster) intentionally carry zero
      // footprint — they're not bootstrap-kit reconciled.
      expect(footprintFor('opentofu')).toEqual({ ramMb: 0, cpuMilli: 0 })
      expect(footprintFor('vcluster')).toEqual({ ramMb: 0, cpuMilli: 0 })
      expect(footprintFor('does-not-exist')).toEqual({ ramMb: 0, cpuMilli: 0 })
    })
  })

  describe('sumFootprints', () => {
    it('sums multiple components correctly', () => {
      const ids = ['cilium', 'flux', 'cert-manager']
      const total = sumFootprints(ids)
      const expectedRam =
        COMPONENT_FOOTPRINTS['cilium']!.ramMb +
        COMPONENT_FOOTPRINTS['flux']!.ramMb +
        COMPONENT_FOOTPRINTS['cert-manager']!.ramMb
      const expectedCpu =
        COMPONENT_FOOTPRINTS['cilium']!.cpuMilli +
        COMPONENT_FOOTPRINTS['flux']!.cpuMilli +
        COMPONENT_FOOTPRINTS['cert-manager']!.cpuMilli
      expect(total.ramMb).toBe(expectedRam)
      expect(total.cpuMilli).toBe(expectedCpu)
    })

    it('returns zero when fed an empty list', () => {
      expect(sumFootprints([])).toEqual({ ramMb: 0, cpuMilli: 0 })
    })

    it('treats unknown ids as zero (no throw)', () => {
      const total = sumFootprints(['cilium', 'mystery-component'])
      expect(total).toEqual(COMPONENT_FOOTPRINTS['cilium'])
    })
  })

  describe('workerCapacity', () => {
    it('converts vCPU + GB into the same shape footprintFor returns', () => {
      const cap = workerCapacity(4, 8)
      expect(cap.cpuMilli).toBe(4000)
      expect(cap.ramMb).toBe(8 * 1024)
    })
  })

  describe('recommendedWorkers', () => {
    it('returns 0 when total footprint is empty (no selection yet)', () => {
      expect(recommendedWorkers(workerCapacity(4, 8), { ramMb: 0, cpuMilli: 0 })).toBe(0)
    })

    it('binds on RAM when RAM is the larger constraint', () => {
      // Per worker: 8 GiB / 4 vCPU. Need 14 GiB / 2 vCPU →
      //   ceil(14/8) = 2 workers (RAM)
      //   ceil(2000/4000) = 1 worker (CPU)
      //   max = 2.
      const cap = workerCapacity(4, 8)
      expect(recommendedWorkers(cap, { ramMb: 14 * 1024, cpuMilli: 2000 })).toBe(2)
    })

    it('binds on CPU when CPU is the larger constraint', () => {
      // Per worker: 16 GiB / 2 vCPU. Need 4 GiB / 8 vCPU →
      //   ceil(4/16) = 1 worker (RAM)
      //   ceil(8000/2000) = 4 workers (CPU)
      //   max = 4.
      const cap = workerCapacity(2, 16)
      expect(recommendedWorkers(cap, { ramMb: 4 * 1024, cpuMilli: 8000 })).toBe(4)
    })

    it('always returns at least 1 when total footprint is non-zero', () => {
      const cap = workerCapacity(16, 32)
      expect(recommendedWorkers(cap, { ramMb: 64, cpuMilli: 100 })).toBe(1)
    })

    it('reproduces the otech92 evidence — 2 workers insufficient for 14 GB bootstrap', () => {
      // Live evidence motivating the issue: 2× cpx32 (4 vCPU / 8 GiB each
      // = 16 GiB / 8 vCPU pool) couldn't fit a ~14 GiB bootstrap-kit because
      // the request floor hit the per-node bin-packing bound. The
      // recommender should call for 2 workers MINIMUM at exactly the
      // ramMb=14336 threshold (= 14 GiB, ceil(14/8)=2). Bumping one app to
      // ~17 GiB total request load raises the recommendation to 3 workers
      // — matching the founder's "bump to 3 workers" guidance in the
      // issue description.
      const cap = workerCapacity(4, 8) // cpx32
      expect(recommendedWorkers(cap, { ramMb: 14 * 1024, cpuMilli: 4000 })).toBe(2)
      expect(recommendedWorkers(cap, { ramMb: 17 * 1024, cpuMilli: 4000 })).toBe(3)
    })
  })

  describe('CONTROL_PLANE_OVERHEAD', () => {
    it('is non-zero (k3s control plane consumes resources)', () => {
      expect(CONTROL_PLANE_OVERHEAD.ramMb).toBeGreaterThan(0)
      expect(CONTROL_PLANE_OVERHEAD.cpuMilli).toBeGreaterThan(0)
    })
  })
})
