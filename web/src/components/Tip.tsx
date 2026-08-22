import { useState } from 'react'

interface TipProps {
  tip: string
  children: React.ReactNode
  className?: string
}

// Tip wraps content and shows a tooltip that follows the mouse. The tooltip is
// rendered with position:fixed at the cursor, so it is never clipped by
// overflow:auto scroll containers (unlike pure-CSS ::after tooltips, which get
// cut off inside scrollable lists). See index.css .tip-pop.
export function Tip({ tip, children, className }: TipProps) {
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null)
  const move = (e: React.MouseEvent) => setPos({ x: e.clientX, y: e.clientY })
  const leave = () => setPos(null)

  return (
    <span
      className={`tip ${className || ''}`}
      onMouseEnter={move}
      onMouseMove={move}
      onMouseLeave={leave}
    >
      {children}
      {pos && (
        <span className="tip-pop" style={{ left: pos.x + 12, top: pos.y + 14 }}>
          {tip}
        </span>
      )}
    </span>
  )
}