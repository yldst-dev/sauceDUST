"""유스케이스 검증.

포트 덕분에 torch 없이 돕니다. 가짜 인코더를 꽂아 전체 흐름을 확인합니다.
"""

from __future__ import annotations

import asyncio
import base64
import io

import numpy as np
import pytest
from PIL import Image

from config import Settings
from domain import ModelSpec
from service import Batcher, EmbedService

SPECS = [
    ModelSpec(id="copy-model", kind="copy", backend="fake",
              checkpoint="fake/copy", vector_size=8),
    ModelSpec(id="semantic-model", kind="semantic", backend="fake",
              checkpoint="fake/semantic", vector_size=8),
]

SETTINGS = Settings(specs=SPECS, thumb_size=384, thumb_quality=85,
                    batch_window_ms=5, batch_size_override=0)


class FakeEncoder:
    """이미지 평균 밝기에서 결정적으로 벡터를 만듭니다."""

    def __init__(self, spec: ModelSpec):
        self._spec = spec
        self.batches: list[int] = []
        self.fail = False

    @property
    def spec(self) -> ModelSpec:
        return self._spec

    def encode(self, images):
        if self.fail:
            raise RuntimeError("모델이 뻗었습니다")
        self.batches.append(len(images))

        out = np.zeros((len(images), self._spec.vector_size), dtype=np.float32)
        for i, image in enumerate(images):
            seed = float(np.asarray(image.convert("L"), dtype=np.float32).mean())
            out[i] = [(seed + j) % 17 for j in range(self._spec.vector_size)]
            out[i] /= np.linalg.norm(out[i])
        return out


def make_service(**kwargs) -> tuple[EmbedService, list[FakeEncoder]]:
    encoders = [FakeEncoder(spec) for spec in SPECS]
    settings = SETTINGS
    if kwargs:
        settings = Settings(**{**SETTINGS.__dict__, **kwargs})
    return EmbedService(encoders, settings, "cpu", batch_size=4), encoders


def jpeg_bytes(size: tuple[int, int] = (900, 700), seed: int = 1) -> bytes:
    rng = np.random.default_rng(seed)
    base = rng.random((12, 12, 3)) * 255
    image = Image.fromarray(base.astype(np.uint8), "RGB").resize(size, Image.Resampling.BICUBIC)

    buffer = io.BytesIO()
    image.save(buffer, format="JPEG", quality=90)
    return buffer.getvalue()


def test_service_requires_encoders():
    with pytest.raises(ValueError):
        EmbedService([], SETTINGS, "cpu", 4)


def test_info_reports_every_model():
    service, _ = make_service()
    payload = service.info().to_payload()

    assert payload["device"] == "cpu"
    assert [m["id"] for m in payload["models"]] == ["copy-model", "semantic-model"]
    assert all(m["vector_size"] == 8 for m in payload["models"])


def test_prepare_produces_hashes_and_thumb():
    service, _ = make_service()
    prepared = service.prepare(jpeg_bytes(), want_thumb=True)

    assert len(prepared.hashes.phash) == 16
    assert len(prepared.hashes.dhash) == 16
    assert prepared.thumb and len(prepared.thumb) > 0
    assert prepared.derived.width == 900
    assert prepared.derived.height == 700


# 검색 질의에는 축소본이 필요 없습니다. 만들면 그냥 버려집니다.
def test_prepare_skips_thumb_when_not_wanted():
    service, _ = make_service()
    prepared = service.prepare(jpeg_bytes(), want_thumb=False)
    assert prepared.thumb is None


def test_assemble_carries_every_model_vector():
    service, _ = make_service()
    prepared = service.prepare(jpeg_bytes(), want_thumb=True)
    vectors = service.encode([prepared.derived.prep])

    result = service.assemble(prepared, {k: v[0] for k, v in vectors.items()})
    payload = result.to_payload()

    assert {v["model_id"] for v in payload["vectors"]} == {"copy-model", "semantic-model"}
    for wire in payload["vectors"]:
        assert wire["size"] == 8


# 벡터는 JSON 숫자 배열이 아니라 float32 원본 바이트로 나가야 합니다.
def test_payload_encodes_vectors_as_float32_bytes():
    service, _ = make_service()
    prepared = service.prepare(jpeg_bytes(), want_thumb=False)
    vectors = service.encode([prepared.derived.prep])
    result = service.assemble(prepared, {k: v[0] for k, v in vectors.items()})

    wire = result.to_payload()["vectors"][0]
    raw = base64.b64decode(wire["data"])

    assert len(raw) == 8 * 4
    restored = np.frombuffer(raw, dtype="<f4")
    np.testing.assert_allclose(restored, vectors[wire["model_id"]][0], rtol=1e-6)


def test_payload_omits_thumb_when_absent():
    service, _ = make_service()
    prepared = service.prepare(jpeg_bytes(), want_thumb=False)
    result = service.assemble(prepared, {"copy-model": np.zeros(8, dtype=np.float32)})

    assert result.to_payload()["thumb_base64"] == ""


def test_encode_runs_every_model_once_per_batch():
    service, encoders = make_service()
    images = [service.prepare(jpeg_bytes(seed=i), want_thumb=False).derived.prep
              for i in range(3)]

    service.encode(images)
    for encoder in encoders:
        assert encoder.batches == [3], f"{encoder.spec.id}가 {encoder.batches}로 불렸습니다"


# 배칭 검증 -----------------------------------------------------------------

async def gather_batch(service, count: int, window_ms: int = 30):
    batcher = Batcher(
        run_batch=service.encode, size=8, window_ms=window_ms,
        to_thread=lambda fn, *args: asyncio.to_thread(fn, *args),
    )
    batcher.start()

    images = [service.prepare(jpeg_bytes(seed=i), want_thumb=False).derived.prep
              for i in range(count)]
    try:
        return await asyncio.gather(*(batcher.submit(image) for image in images))
    finally:
        await batcher.stop()


def test_batcher_groups_concurrent_requests():
    """동시에 온 요청은 한 묶음으로 추론해야 GPU가 놀지 않습니다."""
    service, encoders = make_service()
    results = asyncio.run(gather_batch(service, 6))

    assert len(results) == 6
    assert encoders[0].batches == [6], f"묶음이 {encoders[0].batches}입니다"


def test_batcher_returns_matching_vector_per_request():
    """묶음으로 처리해도 각 요청은 자기 벡터를 받아야 합니다."""
    service, _ = make_service()
    results = asyncio.run(gather_batch(service, 4))

    seen = {tuple(np.round(r["copy-model"], 5)) for r in results}
    assert len(seen) == 4, "서로 다른 이미지인데 같은 벡터가 나왔습니다"


def test_batcher_propagates_failure_to_every_waiter():
    service, encoders = make_service()
    for encoder in encoders:
        encoder.fail = True

    with pytest.raises(RuntimeError):
        asyncio.run(gather_batch(service, 3))


def test_batcher_handles_single_request():
    service, encoders = make_service()
    results = asyncio.run(gather_batch(service, 1))

    assert len(results) == 1
    assert encoders[0].batches == [1]
