interface OOLogoProps {
  h?: number
  c1?: string
  c2?: string
  id?: string
}

export function OOLogo({ h = 28, c1 = '#38BDF8', c2 = '#818CF8', id = 'oo-logo' }: OOLogoProps) {
  const w = Math.round(h * 1.75)
  return (
    <svg width={w} height={h} viewBox="0 0 700 400" fill="none" aria-hidden="true" style={{ flexShrink: 0 }}>
      <defs>
        <linearGradient id={id} x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor={c1} />
          <stop offset="100%" stopColor={c2} />
        </linearGradient>
      </defs>
      <path
        d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
        fill="none"
        stroke={`url(#${id})`}
        strokeWidth="100"
        strokeLinecap="butt"
      />
    </svg>
  )
}
