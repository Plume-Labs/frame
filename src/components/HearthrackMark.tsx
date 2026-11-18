import { useId } from 'react'

/**
 * The Hearthrack icon (charter, track D · hybrid): two rack rails framing a
 * flame cut into rack units. Drawn inline so it follows the console theme —
 * the rails and mounting holes use the foreground/background tokens, the flame
 * keeps its fixed ember gradient. Use the icon alone below 48 px.
 */
export function HearthrackMark({ size = 24, className }: { size?: number; className?: string }) {
  // Gradient and mask ids must be unique per instance: the sidebar and the
  // login card can both be mounted, and a duplicate id would reuse the first.
  const uid = useId().replace(/:/g, '')
  const grad = `hr-grad-${uid}`
  const mask = `hr-mask-${uid}`
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 64 64"
      width={size}
      height={size}
      role="img"
      aria-label="Hearthrack"
      className={className}
    >
      <defs>
        <linearGradient id={grad} x1="0" y1="1" x2="0" y2="0">
          <stop offset="0" stopColor="#E8452C" />
          <stop offset="1" stopColor="#FFB43A" />
        </linearGradient>
        <mask id={mask}>
          <path
            d="M35 2 C35 18 53 26 53 42 C53 53 44 62 32 62 C20 62 11 53 11 42 C11 33 16 27 21 21 C21 28 24 33 28 34 C26 22 30 10 35 2 Z"
            fill="#fff"
          />
          <rect x="0" y="29" width="64" height="3.2" fill="#000" />
          <rect x="0" y="40" width="64" height="3.2" fill="#000" />
          <rect x="0" y="51" width="64" height="3.2" fill="#000" />
        </mask>
      </defs>
      <rect x="5" y="5" width="9" height="54" rx="3" fill="var(--foreground)" />
      <rect x="50" y="5" width="9" height="54" rx="3" fill="var(--foreground)" />
      {[13, 32, 51].map((cy) => (
        <g key={cy} fill="var(--background)">
          <circle cx="9.5" cy={cy} r="1.5" />
          <circle cx="54.5" cy={cy} r="1.5" />
        </g>
      ))}
      <g transform="translate(6.4 6.4) scale(.8)">
        <rect width="64" height="64" fill={`url(#${grad})`} mask={`url(#${mask})`} />
      </g>
    </svg>
  )
}

/** Lowercase, tight wordmark — charter: Inter 700, letter-spacing -0.02em. */
export function HearthrackWordmark({ className }: { className?: string }) {
  return (
    <span className={`font-bold tracking-[-0.02em] ${className ?? ''}`}>hearthrack</span>
  )
}
