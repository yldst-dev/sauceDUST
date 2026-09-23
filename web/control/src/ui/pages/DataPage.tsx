import { useEffect, useState } from "react"
import { loadModels, lookupPost, searchImage } from "../../application/console"
import type { ModelRow, SearchHit, Stats } from "../../domain/types"
import { Button } from "../components/Button"
import { Card } from "../components/Card"
import { Field, TextInput } from "../components/Field"
import { PageHeader } from "../components/PageHeader"
import { Table } from "../components/Table"
import { num } from "../format"

export function DataPage({ stats, onFlash }: { stats: Stats | null; onFlash: (message: string) => void }) {
  const [models, setModels] = useState<ModelRow[]>([])
  const [postId, setPostId] = useState("")
  const [lookup, setLookup] = useState("")
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const vectors = stats?.vectors ?? {}

  useEffect(() => {
    loadModels().then(setModels).catch((err: Error) => onFlash(err.message))
  }, [onFlash])

  return (
    <>
      <PageHeader title="데이터" lead="쌓인 벡터를 보고, 게시물을 찾거나 이미지로 검색합니다." />
      <div className="grid-2">
        <Card title="모델">
          <Table columns={[{ label: "모델" }, { label: "용도" }, { label: "차원", num: true }, { label: "벡터", num: true }]}>
            {models.map((model) => (
              <tr key={model.id}>
                <td>{model.id}</td>
                <td>{model.kind}</td>
                <td className="num">{num(model.vector_size)}</td>
                <td className="num">{num(vectors[model.id] || 0)}</td>
              </tr>
            ))}
          </Table>
          {models.length === 0 ? <p className="empty">등록된 모델이 없습니다.</p> : null}
        </Card>
        <Card title="게시물 조회">
          <form onSubmit={(e) => {
            e.preventDefault()
            const site = stats?.source_site || "danbooru"
            lookupPost(site, postId.trim())
              .then((img) => setLookup(img?.canonical_url || "아직 수집되지 않았습니다."))
              .catch((err: Error) => setLookup(err.message))
          }}>
            <Field label="게시물 번호">
              <TextInput inputMode="numeric" required value={postId} onChange={(e) => setPostId(e.target.value)} />
            </Field>
            <Button tone="primary" type="submit">찾아보기</Button>
          </form>
          {lookup ? <p className="hit">{lookup.startsWith("http") ? <a href={lookup}>{lookup}</a> : lookup}</p> : null}
        </Card>
      </div>
      <Card title="이미지로 검색">
        <form onSubmit={(e) => {
          e.preventDefault()
          const file = (e.currentTarget.elements.namedItem("file") as HTMLInputElement).files?.[0]
          if (!file) return
          searchImage(file).then((res) => setHits(res.hits ?? [])).catch((err: Error) => onFlash(err.message))
        }}>
          <Field label="이미지 파일">
            <TextInput name="file" type="file" accept="image/*" required />
          </Field>
          <Button tone="primary" type="submit">검색</Button>
        </form>
        {hits && hits.length === 0 ? <p className="empty">맞는 결과가 없습니다.</p> : null}
        {hits?.map((hit) => (
          <div className="hit" key={`${hit.source_post_id}-${hit.score}`}>
            <a href={hit.canonical_url}>{hit.canonical_url || hit.source_post_id}</a> · {Number(hit.score).toFixed(3)}
          </div>
        ))}
      </Card>
    </>
  )
}
