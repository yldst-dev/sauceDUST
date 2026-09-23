import type { ReactNode } from "react"

export function Card({ title, extra, children }: { title?: string; extra?: ReactNode; children: ReactNode }) {
  return (
    <article className="card">
      {title || extra ? (
        <div className="card-head">
          {title ? <h3>{title}</h3> : <span />}
          {extra}
        </div>
      ) : null}
      {children}
    </article>
  )
}
