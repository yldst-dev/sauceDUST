export function PageHeader({ title, lead }: { title: string; lead: string }) {
  return (
    <>
      <h2>{title}</h2>
      <p className="lead">{lead}</p>
    </>
  )
}
