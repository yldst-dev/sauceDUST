import type { ReactNode } from "react"

export function Table({ columns, children }: { columns: { label: string; num?: boolean }[]; children: ReactNode }) {
  return (
    <table>
      <thead>
        <tr>
          {columns.map((col) => (
            <th key={col.label} className={col.num ? "num" : undefined}>{col.label}</th>
          ))}
        </tr>
      </thead>
      <tbody>{children}</tbody>
    </table>
  )
}
