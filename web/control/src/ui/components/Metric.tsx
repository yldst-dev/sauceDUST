export function Metric({ label, value, unit, tone }: { label: string; value: string; unit?: string; tone?: string }) {
  return (
    <div>
      <div className="k">{label}</div>
      <div className={tone ? `v ${tone}` : "v"}>{value}</div>
      {unit ? <span className="u">{unit}</span> : null}
    </div>
  )
}
