export function PageHeader({ title, lead }: { title: string; lead: string }) {
  return (
    <div className="page-head">
      <div>
        <h2>{title}</h2>
        <p className="lead">{lead}</p>
      </div>
    </div>
  )
}
