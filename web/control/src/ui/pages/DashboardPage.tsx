import { coverage, phase, remainingRanges } from "../../domain/progress"
import type { NodeRow, Stats } from "../../domain/types"
import { Card } from "../components/Card"
import { Metric } from "../components/Metric"
import { PageHeader } from "../components/PageHeader"
import { Pill, statusLabel } from "../components/Pill"
import { ProgressBar } from "../components/ProgressBar"
import { Table } from "../components/Table"
import { dash, num, rate, timeText } from "../format"

export function DashboardPage({ stats, nodes }: { stats: Stats | null; nodes: NodeRow[] }) {
  const label = stats ? phase(stats) : "대기"
  const cover = stats ? coverage(stats) : 0
  const left = stats ? remainingRanges(stats) : 0
  return (
    <>
      <PageHeader title="대시보드" lead="수집 작업의 진행 상황과 노드 상태를 실시간으로 확인할 수 있습니다." />
      <Card>
        <div className="card-head">
          <div className="eyebrow">수집 구간</div>
          <Pill tone={label === "대기" ? "wait" : "ok"}>{label}</Pill>
        </div>
        <h3>수집 구간 1</h3>
        <div className="metrics">
          <Metric label="총 대상" value={stats ? num(stats.high_watermark) : "—"} unit="이미지" />
          <Metric label="수집한 이미지" value={stats ? num(stats.images) : "—"} unit="이미지" />
          <Metric label="남은 구간" value={stats ? num(left) : "—"} unit="구간" />
          <Metric label="흡수율" value={stats ? `${cover.toFixed(1)}%` : "—"} tone="teal" />
        </div>
        <ProgressBar value={cover} />
        <div className="bar-meta">
          <span>남은 구간 {stats ? num(left) : "—"}</span>
          <span>{stats ? `${num(stats.high_watermark)} 중 ${num(stats.images)} 수집 완료` : "—"}</span>
        </div>
      </Card>
      <Card title="노드" extra={<span className="eyebrow">총 {nodes.length}개 노드</span>}>
        <p className="sub">수집을 수행하는 노드의 상태와 처리 성능을 확인합니다.</p>
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
      </Card>
      <Card title="진행 상태" extra={<span className="eyebrow">최근 업데이트 <b>{timeText()}</b></span>}>
        <p className="sub">전체 수집 작업의 진행 현황을 확인합니다.</p>
        <div className="tiles">
          <div className="tile"><dt>완료 구간</dt><dd>{stats ? num(stats.ranges_done) : "—"}</dd></div>
          <div className="tile"><dt>처리 중</dt><dd>{stats ? num(stats.ranges_active) : "—"}</dd></div>
          <div className="tile"><dt className="alarm">실패 구간</dt><dd className="alarm">{stats ? num(stats.ranges_failed) : "—"}</dd></div>
          <div className="tile"><dt className="warn">재시도 대기</dt><dd className="warn">{stats ? num(stats.retry_queue) : "—"}</dd></div>
          <div className="tile"><dt>전체 초당</dt><dd>{stats ? rate(stats.saved_per_sec) : "—"}</dd></div>
        </div>
      </Card>
    </>
  )
}
