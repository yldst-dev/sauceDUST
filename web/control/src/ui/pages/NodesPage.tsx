import { reclaimNodes } from "../../application/console"
import type { NodeRow } from "../../domain/types"
import { Button } from "../components/Button"
import { Card } from "../components/Card"
import { PageHeader } from "../components/PageHeader"
import { Pill, statusLabel } from "../components/Pill"
import { Table } from "../components/Table"
import { dash, num, rate } from "../format"

export function NodesPage({ nodes, onFlash, onChanged }: { nodes: NodeRow[]; onFlash: (message: string) => void; onChanged: () => void }) {
  return (
    <>
      <PageHeader title="노드" lead="살아 있는 노드와 최근 처리량입니다." />
      <div className="actions">
        <Button type="button" onClick={() => reclaimNodes().then((b) => { onFlash(`${num(b.count)}개 임대를 회수했습니다`); onChanged() }).catch((err: Error) => onFlash(err.message))}>끊긴 노드의 임대 회수</Button>
      </div>
      <Card>
        <Table columns={[
          { label: "이름" }, { label: "역할" }, { label: "호스트" }, { label: "버전" }, { label: "장치" }, { label: "경로" },
          { label: "CPU", num: true }, { label: "메모리", num: true }, { label: "마지막 신호", num: true }, { label: "상태" },
        ]}>
          {nodes.map((node) => {
            const status = statusLabel(node.status)
            const seen = node.last_seen_sec == null ? "—" : `${Math.max(0, Math.round(node.last_seen_sec))}초 전`
            return (
              <tr key={node.id}>
                <td>{node.id}</td>
                <td>{node.role}</td>
                <td>{dash(node.hostname)}</td>
                <td>{dash(node.version)}</td>
                <td>{dash(node.device)}</td>
                <td>{node.net_mode ? (node.net_mode === "direct" ? <span className="path">direct</span> : node.net_mode) : "—"}</td>
                <td className="num">{rate(node.cpu_pct)}%</td>
                <td className="num">{num(node.mem_mb)}</td>
                <td className="num">{seen}</td>
                <td><Pill tone={status.tone}>{status.text}</Pill></td>
              </tr>
            )
          })}
        </Table>
        {nodes.length === 0 ? <p className="empty">등록된 노드가 없습니다.</p> : null}
      </Card>
    </>
  )
}
