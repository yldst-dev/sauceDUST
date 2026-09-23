import { useEffect, useState } from "react"
import { fillGaps, loadJobs, resetRange } from "../../application/console"
import type { Jobs } from "../../domain/types"
import { Button } from "../components/Button"
import { Card } from "../components/Card"
import { Metric } from "../components/Metric"
import { PageHeader } from "../components/PageHeader"
import { Table } from "../components/Table"
import { num } from "../format"

export function JobsPage({ onFlash }: { onFlash: (message: string) => void }) {
  const [jobs, setJobs] = useState<Jobs | null>(null)

  function refresh() {
    loadJobs().then(setJobs).catch((err: Error) => onFlash(err.message))
  }

  useEffect(() => { refresh() }, [])

  const meta = jobs
    ? [
        `재시도 대기 ${num(jobs.retry_pending)}`,
        `죽은 재시도 ${num(jobs.retry_dead)}`,
        `비어 있음 ${num(jobs.empty)}`,
        jobs.stale_running ? `${num(jobs.stale_running)}개가 30분 넘게 멈춰 있습니다` : "",
        jobs.missing_ids ? `빠지는 ID 약 ${num(jobs.missing_ids)}` : "",
      ].filter(Boolean).join(" · ")
    : ""

  return (
    <>
      <PageHeader title="작업" lead="수집 구간을 보고, 막힌 구간을 다시 돌립니다." />
      <Card>
        <div className="metrics">
          <Metric label="전체 구간" value={jobs ? num(jobs.total) : "—"} />
          <Metric label="완료" value={jobs ? num(jobs.completed) : "—"} />
          <Metric label="처리 중" value={jobs ? num(jobs.running) : "—"} />
          <Metric label="재시도 소진" value={jobs ? num(jobs.exhausted) : "—"} tone="alarm" />
        </div>
        <p className="sub">{meta}</p>
        {jobs?.manage ? (
          <div className="actions">
            <Button tone="primary" type="button" onClick={() => resetRange().then((b) => { onFlash(`${num(b.count)}개 구간을 되살렸습니다`); refresh() }).catch((err: Error) => onFlash(err.message))}>소진된 구간 되살리기</Button>
            <Button type="button" onClick={() => resetRange(0, true).then((b) => { onFlash(`구간 ${num(b.count)}개, 재시도 ${num(b.retries)}개를 되살렸습니다`); refresh() }).catch((err: Error) => onFlash(err.message))}>죽은 재시도도 되살리기</Button>
            <Button type="button" onClick={() => fillGaps().then((b) => { onFlash(`${num(b.count)}개 구간을 넣었습니다`); refresh() }).catch((err: Error) => onFlash(err.message))}>빈 구간 채우기</Button>
          </div>
        ) : null}
        <Table columns={[{ label: "구간" }, { label: "ID 대역" }, { label: "개수", num: true }, { label: "시도", num: true }, { label: "마지막 오류" }, { label: "" }]}>
          {(jobs?.failed_ranges ?? []).map((row) => (
            <tr key={row.id}>
              <td>{num(row.id)}</td>
              <td>{num(row.lower)}~{num(row.upper)}</td>
              <td className="num">{num(row.count)}</td>
              <td className="num">{num(row.attempts)}</td>
              <td>{row.error}</td>
              <td><Button type="button" onClick={() => resetRange(row.id).then(() => { onFlash("구간을 되살렸습니다"); refresh() }).catch((err: Error) => onFlash(err.message))}>되살리기</Button></td>
            </tr>
          ))}
        </Table>
        {(jobs?.failed_ranges.length ?? 0) === 0 ? <p className="empty">소진된 구간이 없습니다.</p> : null}
      </Card>
      <Card title="빈 구간">
        <Table columns={[{ label: "시작" }, { label: "끝" }, { label: "개수", num: true }]}>
          {(jobs?.gaps ?? []).map((gap) => (
            <tr key={`${gap.from}-${gap.to}`}>
              <td>{num(gap.from)}</td>
              <td>{num(gap.to)}</td>
              <td className="num">{num(gap.count)}</td>
            </tr>
          ))}
        </Table>
        {(jobs?.gaps.length ?? 0) === 0 ? <p className="empty">빈 구간이 없습니다.</p> : null}
      </Card>
    </>
  )
}
