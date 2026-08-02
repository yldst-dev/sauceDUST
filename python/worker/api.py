"""HTTP 전송 계층. 요청을 유스케이스에 넘기고 결과를 형식에 맞춰 돌려줄 뿐입니다."""

from __future__ import annotations

import logging
import os
from contextlib import asynccontextmanager
from dataclasses import dataclass

import anyio
from fastapi import FastAPI, File, HTTPException, Query, UploadFile

from domain.imaging import ImageError
from service import Batcher, EmbedService

log = logging.getLogger("saucedust.worker")

MAX_UPLOAD_BYTES = 32 << 20


def prepare_slots() -> int:
    """준비 단계가 동시에 쓸 수 있는 스레드 수입니다.

    준비는 CPU를 꽉 씁니다. 요청 수만큼 풀어 두면 코어를 전부 차지해서
    정작 추론 쪽이 CPU를 못 받습니다. 추론은 GPU에 보내기 전 전처리에도
    CPU가 필요하므로, 코어를 조금 남겨 두어야 GPU가 놀지 않습니다.
    """
    if override := os.getenv("EMBED_PREPARE_THREADS", "").strip():
        return max(1, int(override))
    return max(2, (os.cpu_count() or 4) - 2)


@dataclass
class _State:
    """적재가 끝난 뒤에 채워지는 것들입니다.

    타입 없는 딕셔너리로 두면 검사기가 아무것도 확인해 주지 못합니다.
    """

    service: EmbedService | None = None
    batcher: Batcher | None = None


def create_app(load_service, window_ms: int) -> FastAPI:
    """load_service는 모델을 올리고 EmbedService를 돌려주는 함수입니다.

    적재를 함수로 받는 이유는 시험에서 가짜 서비스를 꽂기 위해서입니다.
    """
    state = _State()

    # 준비와 추론이 스레드 자리를 두고 다투지 않게 각자의 몫을 정해 둡니다.
    prepare_limiter = anyio.CapacityLimiter(prepare_slots())
    infer_limiter = anyio.CapacityLimiter(1)

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        service: EmbedService = await anyio.to_thread.run_sync(load_service)

        async def run_inference(fn, images):
            return await anyio.to_thread.run_sync(fn, images, limiter=infer_limiter)

        batcher = Batcher(
            run_batch=service.encode,
            size=service.batch_size,
            window_ms=window_ms,
            to_thread=run_inference,
        )
        batcher.start()

        state.service = service
        state.batcher = batcher
        log.info("모델 %d개를 %s에 올렸습니다",
                 len(service.info().specs), service.info().device)
        yield

        await batcher.stop()

    app = FastAPI(title="saucedust embedding worker", lifespan=lifespan)

    def ready() -> tuple[EmbedService, Batcher]:
        if state.service is None or state.batcher is None:
            raise HTTPException(status_code=503, detail="모델을 아직 올리는 중입니다")
        return state.service, state.batcher

    @app.get("/health")
    async def health() -> dict:
        service, batcher = state.service, state.batcher
        return {
            "ready": service is not None,
            "device": service.info().device if service else "unknown",
            "queued": batcher.queued if batcher else 0,
            "inflight": batcher.inflight if batcher else 0,
            "avg_batch": round(batcher.avg_batch, 2) if batcher else 0.0,
        }

    @app.get("/info")
    async def info() -> dict:
        service, _ = ready()
        return service.info().to_payload()

    @app.post("/embed")
    async def embed(
        # FastAPI는 의존성을 기본값 자리에 두는 것이 공식 방식입니다.
        file: UploadFile = File(...),  # noqa: B008
        thumb: bool = Query(True, description="축소본이 필요 없으면 0으로 두십시오"),
    ) -> dict:
        service, batcher = ready()

        raw = await file.read()
        if len(raw) > MAX_UPLOAD_BYTES:
            raise HTTPException(status_code=413, detail="이미지가 너무 큽니다")

        # CPU 갈래는 요청마다 스레드에서 병렬로 돕니다.
        try:
            prepared = await anyio.to_thread.run_sync(
                service.prepare, raw, thumb, limiter=prepare_limiter
            )
        except ImageError as exc:
            raise HTTPException(status_code=400, detail=str(exc)) from exc

        # GPU 갈래는 짧게 모아 배치로 처리합니다.
        try:
            vectors = await batcher.submit(prepared.derived.prep)
        except Exception as exc:
            log.exception("임베딩 실패")
            raise HTTPException(status_code=500, detail=str(exc)) from exc

        return service.assemble(prepared, vectors).to_payload()

    return app
