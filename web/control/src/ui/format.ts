const nf = new Intl.NumberFormat("ko-KR")

export function num(value: number | null | undefined) {
  return nf.format(Number(value) || 0)
}

export function rate(value: number | null | undefined) {
  return (Number(value) || 0).toFixed(1)
}

export function clockText(date = new Date()) {
  const p = (n: number) => String(n).padStart(2, "0")
  return `${date.getFullYear()}. ${p(date.getMonth() + 1)}. ${p(date.getDate())} ${p(date.getHours())}:${p(date.getMinutes())}:${p(date.getSeconds())}`
}

export function timeText(date = new Date()) {
  const p = (n: number) => String(n).padStart(2, "0")
  return `${p(date.getHours())}:${p(date.getMinutes())}:${p(date.getSeconds())}`
}

export function dash(value: string | null | undefined) {
  return value ? value : "—"
}
