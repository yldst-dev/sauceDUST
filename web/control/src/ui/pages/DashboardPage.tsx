import { useState } from "react"
import { retryWork } from "../../application/console"
import { coverage, phase, remainingRanges } from "../../domain/progress"
import type { NodeRow, Stats } from "../../domain/types"
import { Button } from "../components/Button"
import { Card } from "../components/Card"
import { Metric } from "../components/Metric"
import { Pill, statusLabel } from "../components/Pill"
import { ProgressBar } from "../components/ProgressBar"
import { Table } from "../components/Table"
import { dash, num, rate, timeText } from "../format"

export function DashboardPage({ stats, nodes, onFlash, onChanged }: {
  stats: Stats | null
  nodes: NodeRow[]
  onFlash: (message: string) => void
  onChanged: () => void
}) {
  const [busy, setBusy] = useState<"ranges" | "queue" | "">("")
  const label = stats ? phase(stats) : "대기"
  const cover = stats ? coverage(stats) : 0
  const left = stats ? remainingRanges(stats) : 0
  const failed = stats?.ranges_failed ?? 0
  const queued = stats?.retry_queue ?? 0

  function retry(kind: "ranges" | "queue") {
    setBusy(kind)
    retryWork(kind)
      .then((body) => {
        onFlash(kind === "ranges" ? `실패 구간 ${num(body.count)}개를 다시 돌립니다` : `재시도 대기 ${num(body.count)}개를 바로 돌립니다`)
        onChanged()
      })
      .catch((err: Error) => onFlash(err.message))
      .finally(() => setBusy(""))
  }

  return (
    <div className="stage board">
      <div className="board-top">
        <Card>
          <div className="card-head">
            <div>
              <div className="eyebrow">수집 구간 1</div>
              <strong>{stats ? `${num(stats.images)} / ${num(stats.high_watermark)}` : "—"}</strong>
            </div>
            <Pill tone={label === "대기" ? "wait" : "ok"}>{label}</Pill>
          </div>
          <div className="metrics compact">
            <Metric label="총 대상" value={stats ? num(stats.high_watermark) : "—"} />
            <Metric label="수집" value={stats ? num(stats.images) : "—"} />
            <Metric label="남은 구간" value={stats ? num(left) : "—"} />
            <Metric label="흡수율" value={stats ? `${cover.toFixed(1)}%` : "—"} tone="teal" />
          </div>
          <ProgressBar value={cover} />
        </Card>
        <Card>
          <div className="card-head">
            <h3>진행 상태</h3>
            <span className="eyebrow">{timeText()}</span>
          </div>
          <div className="tiles">
            <div className="tile"><dt>완료</dt><dd>{stats ? num(stats.ranges_done) : "—"}</dd></div>
            <div className="tile"><dt>처리 중</dt><dd>{stats ? num(stats.ranges_active) : "—"}</dd></div>
            <div className="tile">
              <dt className="alarm">실패</dt>
              <dd className="alarm">{stats ? num(failed) : "—"}</dd>
              {failed > 0 ? <Button tone="danger" type="button" disabled={busy !== ""} onClick={() => retry("ranges")}>{busy === "ranges" ? "돌리는 중" : "재시도"}</Button> : null}
            </div>
            <div className="tile">
              <dt className="warn">재시도 대기</dt>
              <dd className="warn">{stats ? num(queued) : "—"}</dd>
              {queued > 0 ? <Button type="button" disabled={busy !== ""} onClick={() => retry("queue")}>{busy === "queue" ? "돌리는 중" : "재시도"}</Button> : null}
            </div>
            <div className="tile"><dt>초당</dt><dd>{stats ? rate(stats.saved_per_sec) : "—"}</dd></div>
          </div>
        </Card>
      </div>
      <Card title="노드" extra={<span className="eyebrow">총 {nodes.length}개</span>}>
        <div className="scroll-panel">
          <Table columns={[
            { label: "이름" }, { label: "역할" }, { label: "장치" }, { label: "경로" },
            { label: "동시", num: true }, { label: "초당", num: true }, { label: "저장", num: true }, { label: "실패", num: true }, { label: "상태" },
          ]}>
            {nodes.map((node) => {
              const status = statusLabel(node.status)
              return (
                <tr key={node.id}>
                  <td>{node.id}</td>
                  <td>{node.role}</td>
                  <td>{dash(node.device)}</td>
                  <td>{node.net_mode ? (node.net_mode === "direct" ? <span className="path">direct</span> : node.net_mode) : "—"}</td>
                  <td className="num">{num(node.concurrency)}</td>
                  <td className="num">{rate(node.per_second)}</td>
                  <td className="num">{num(node.saved)}</td>
                  <td className="num">{num(node.failed)}</td>
                  <td><Pill tone={status.tone}>{status.text}</Pill></td>
                </tr>
              )
            })}
          </Table>
          {nodes.length === 0 ? <p className="empty">등록된 노드가 없습니다.</p> : null}
        </div>
      </Card>
    </div>
  )
}
