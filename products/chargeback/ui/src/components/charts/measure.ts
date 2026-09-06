import { useEffect, useRef, useState, type RefObject } from 'react'

/**
 * useWidth measures the chart container so the SVG viewBox can be drawn at a
 * 1:1 unit-to-pixel scale: axis text stays a true 12 px at any width instead
 * of stretching with a fixed viewBox. The fallback is used before the first
 * layout (and in non-browser renders).
 */
export function useWidth(fallback = 640): { ref: RefObject<HTMLDivElement | null>; width: number } {
  const ref = useRef<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(fallback)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const read = () => {
      const w = el.getBoundingClientRect().width
      if (w > 0) setWidth((prev) => (Math.abs(prev - w) < 0.5 ? prev : Math.round(w)))
    }
    read()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(read)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return { ref, width }
}
