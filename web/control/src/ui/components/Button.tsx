import type { ButtonHTMLAttributes } from "react"

type Tone = "primary" | "ghost" | "danger"

export function Button({ tone = "ghost", className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { tone?: Tone }) {
  return <button className={[tone, className].filter(Boolean).join(" ")} {...props} />
}
