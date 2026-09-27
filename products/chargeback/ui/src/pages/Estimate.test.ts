import { describe, expect, it } from 'vitest'
import { EMBED_MESSAGE, heightMessage, isEmbed, shareLink } from './Estimate'

describe('the share link', () => {
  it('links a saved estimate by the URL the server gave, falling back to the id', () => {
    expect(shareLink({ id: 'e1', share_url: 'https://billing.t99.omani.works/estimate/e1' })).toBe('https://billing.t99.omani.works/estimate/e1')
    expect(shareLink({ id: 'e1' }, 'https://billing.t99.omani.works')).toBe('https://billing.t99.omani.works/estimate/e1')
  })
})

describe('embed mode', () => {
  it('is on only for embed=1 or embed=true', () => {
    expect(isEmbed('?embed=1')).toBe(true)
    expect(isEmbed('embed=1')).toBe(true)
    expect(isEmbed('?region=x&embed=true')).toBe(true)
    expect(isEmbed('?embed=0')).toBe(false)
    expect(isEmbed('?embed=')).toBe(false)
    expect(isEmbed('?embedded=1')).toBe(false)
    expect(isEmbed('')).toBe(false)
  })

  it('reports the height to the parent as a whole number of pixels', () => {
    expect(heightMessage(812.4)).toEqual({ type: EMBED_MESSAGE, height: 813 })
    expect(heightMessage(0)).toEqual({ type: EMBED_MESSAGE, height: 0 })
    expect(heightMessage(-5)).toEqual({ type: EMBED_MESSAGE, height: 0 })
    expect(EMBED_MESSAGE).toBe('openova-estimate-height')
  })
})
