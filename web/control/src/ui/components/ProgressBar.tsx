export function ProgressBar({ value }: { value: number }) {
  const width = Math.max(0, Math.min(100, value))
  return (
    <div className="bar" aria-hidden="true">
      <span style={{ width: `${width}%` }} />
    </div>
  )
}
