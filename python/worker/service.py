"""임베딩 유스케이스.

작업을 두 갈래로 나눕니다.

  CPU 갈래  디코드, 축소, 해시, 축소본 만들기 — 요청마다 스레드에서 병렬
  GPU 갈래  추론 — 짧게 모아 배치로 한 번에

예전에는 배치 스레드 하나가 전부를 순서대로 했습니다. 그동안 GPU가 놀았습니다.
"""

from __future__ import annotations

import asyncio
import contextlib
from collections import deque
from collections.abc import Sequence
from dataclasses import dataclass, field

import numpy as np
from PIL import Image

from config import Settings
from domain import EmbedResult, Hashes, WorkerInfo, hashing, imaging
from ports import Encoder


@dataclass
class Prepared:
    """CPU 갈래가 끝난 상태. 이제 추론만 남았습니다."""

    derived: imaging.Derived
    hashes: Hashes
    thumb: bytes | None


class EmbedService:
    def __init__(self, encoders: Sequence[Encoder], settings: Settings,
                 device_name: str, batch_size: int):
        if not encoders:
            raise ValueError("올린 모델이 없습니다")

        self._encoders = list(encoders)
        self._settings = settings
        self._device_name = device_name
        self._batch_size = batch_size

    @property
    def batch_size(self) -> int:
        return self._batch_size

    def info(self) -> WorkerInfo:
        return WorkerInfo(
            device=self._device_name,
            batch_size=self._batch_size,
            specs=[encoder.spec for encoder in self._encoders],
        )

    def prepare(self, raw: bytes, want_thumb: bool) -> Prepared:
        """CPU 갈래. 원본에서 한 번만 축소하고 나머지를 거기서 뽑습니다."""
        derived = imaging.prepare(raw)
        thumb = None
        if want_thumb:
            thumb = imaging.thumbnail(
                derived, self._settings.thumb_size, self._settings.thumb_quality
            )
        return Prepared(
            derived=derived,
            hashes=hashing.compute(derived.hash_src),
            thumb=thumb,
        )

    def encode(self, images: list[Image.Image]) -> dict[str, np.ndarray]:
        """GPU 갈래. 모든 모델에 같은 묶음을 통과시킵니다."""
        return {encoder.spec.id: encoder.encode(images) for encoder in self._encoders}

    def assemble(self, prepared: Prepared, vectors: dict[str, np.ndarray]) -> EmbedResult:
        return EmbedResult(
            vectors=vectors,
            hashes=prepared.hashes,
            width=prepared.derived.width,
            height=prepared.derived.height,
            thumb=prepared.thumb,
        )


@dataclass
class _Job:
    image: Image.Image
    future: asyncio.Future = field(default_factory=asyncio.Future)


class Batcher:
    """추론 요청을 모아 한 번에 처리합니다.

    GPU는 한 장씩 넣으면 대부분의 시간을 놀며 보냅니다. 그래서 묶어야 합니다.
    그런데 무작정 기다리면 한 건짜리 요청도 그만큼 늦어집니다. 검색 질의가
    바로 그런 경우입니다.

    그래서 아직 처리 중인 요청 수를 봅니다. 지금 꺼낸 것이 전부라면 기다리지
    않고 바로 넘기고, 아직 준비 중인 요청이 남아 있으면 잠깐 기다려 묶음을
    채웁니다. 큐가 비었는지만 보면 안 되는 이유는, 다른 요청들이 아직 준비
    단계에 있어 큐에 들어오지 못한 상태일 수 있기 때문입니다.
    """

    def __init__(self, run_batch, size: int, window_ms: int, to_thread):
        self._run_batch = run_batch
        self._size = max(1, size)
        self._window = max(0.0, window_ms / 1000)
        self._to_thread = to_thread
        self._queue: asyncio.Queue[_Job] = asyncio.Queue()
        self._task: asyncio.Task | None = None
        self._inflight = 0
        # 최근 묶음 크기만 봅니다. 시작 이후 누적 평균은 지나간 부하에 끌려다녀서
        # 지금 상태를 알려주지 못합니다.
        self._recent: deque[int] = deque(maxlen=64)

    @property
    def queued(self) -> int:
        return self._queue.qsize()

    @property
    def inflight(self) -> int:
        return self._inflight

    # 최근 묶음이 얼마나 차는지 보여줍니다. 이 값이 1에 가까운데 부하가 있다면
    # GPU가 한 장씩 처리하고 있다는 뜻입니다.
    @property
    def avg_batch(self) -> float:
        return sum(self._recent) / len(self._recent) if self._recent else 0.0

    def start(self) -> None:
        if self._task is None:
            self._task = asyncio.create_task(self._loop())

    async def stop(self) -> None:
        if self._task is None:
            return
        self._task.cancel()
        # 취소는 우리가 시킨 것이므로 그 예외는 예상된 결과입니다.
        with contextlib.suppress(asyncio.CancelledError):
            await self._task
        self._task = None

    async def submit(self, image: Image.Image) -> dict[str, np.ndarray]:
        self._inflight += 1
        try:
            job = _Job(image=image)
            await self._queue.put(job)
            return await job.future
        finally:
            self._inflight -= 1

    async def _collect(self) -> list[_Job]:
        jobs = [await self._queue.get()]

        # 이미 큐에 와 있는 것은 기다리지 않고 전부 가져갑니다.
        while len(jobs) < self._size and not self._queue.empty():
            jobs.append(self._queue.get_nowait())

        if len(jobs) >= self._size or self._window <= 0:
            return jobs
        # 꺼낸 것이 처리 중인 전부라면 더 올 것이 없습니다.
        if self._inflight <= len(jobs):
            return jobs

        loop = asyncio.get_running_loop()
        deadline = loop.time() + self._window

        while len(jobs) < self._size and self._inflight > len(jobs):
            remaining = deadline - loop.time()
            if remaining <= 0:
                break
            try:
                jobs.append(await asyncio.wait_for(self._queue.get(), remaining))
            except TimeoutError:
                break
        return jobs

    async def _loop(self) -> None:
        while True:
            jobs = await self._collect()
            self._recent.append(len(jobs))
            try:
                vectors = await self._to_thread(self._run_batch, [j.image for j in jobs])
            except Exception as exc:
                self._fail(jobs, exc)
                continue
            self._deliver(jobs, vectors)

    def _deliver(self, jobs: list[_Job], vectors: dict[str, np.ndarray]) -> None:
        for index, job in enumerate(jobs):
            if not job.future.done():
                job.future.set_result(
                    {model_id: values[index] for model_id, values in vectors.items()}
                )

    def _fail(self, jobs: list[_Job], exc: Exception) -> None:
        for job in jobs:
            if not job.future.done():
                job.future.set_exception(exc)
