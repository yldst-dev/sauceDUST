"""모델 설정과 결과 값 객체.

torch 없이 import할 수 있습니다. 그래야 설정 검증과 유스케이스를 무거운
의존성 없이 시험할 수 있습니다.
"""

from __future__ import annotations

import base64
from dataclasses import dataclass, field

import numpy as np

VALID_KINDS = ("copy", "semantic")


class SpecError(ValueError):
    """모델 설정이 잘못되었을 때 냅니다."""


@dataclass(frozen=True)
class ModelSpec:
    id: str
    kind: str
    backend: str
    checkpoint: str
    vector_size: int
    input_size: int = 224

    @classmethod
    def parse(cls, raw: dict) -> ModelSpec:
        required = {"id", "kind", "backend", "checkpoint", "vector_size"}
        missing = required - raw.keys()
        if missing:
            raise SpecError(f"모델 설정에 빠진 항목이 있습니다: {sorted(missing)}")

        kind = raw["kind"]
        if kind not in VALID_KINDS:
            raise SpecError(f"kind는 {' 또는 '.join(VALID_KINDS)}여야 합니다: {kind!r}")

        vector_size = int(raw["vector_size"])
        if vector_size <= 0:
            raise SpecError(f"vector_size는 1 이상이어야 합니다: {vector_size}")

        input_size = int(raw.get("input_size", 224))
        if input_size <= 0:
            raise SpecError(f"input_size는 1 이상이어야 합니다: {input_size}")

        return cls(
            id=str(raw["id"]),
            kind=kind,
            backend=str(raw["backend"]),
            checkpoint=str(raw["checkpoint"]),
            vector_size=vector_size,
            input_size=input_size,
        )

    def as_dict(self, device: str) -> dict:
        return {
            "id": self.id,
            "kind": self.kind,
            "backend": device,
            "checkpoint": self.checkpoint,
            "vector_size": self.vector_size,
            "input_size": self.input_size,
        }


def parse_specs(raw: list) -> list[ModelSpec]:
    specs = [ModelSpec.parse(item) for item in raw]
    if not specs:
        raise SpecError("적어도 모델 하나는 설정해야 합니다")

    seen: set[str] = set()
    for spec in specs:
        if spec.id in seen:
            raise SpecError(f"모델 식별자가 겹칩니다: {spec.id}")
        seen.add(spec.id)
    return specs


@dataclass
class Hashes:
    phash: str
    dhash: str


@dataclass
class EmbedResult:
    """이미지 한 장의 처리 결과."""

    vectors: dict[str, np.ndarray]
    hashes: Hashes
    width: int
    height: int
    thumb: bytes | None = None

    def to_payload(self) -> dict:
        """전송 형식으로 바꿉니다.

        벡터는 JSON 숫자 배열 대신 float32 원본 바이트를 base64로 보냅니다.
        768차원 기준 숫자 배열은 약 9KB, 이 방식은 4KB입니다.
        Python의 float 직렬화와 Go의 float 파싱을 둘 다 건너뜁니다.
        """
        return {
            "vectors": [
                {
                    "model_id": model_id,
                    "size": int(values.shape[0]),
                    "data": base64.b64encode(
                        np.ascontiguousarray(values, dtype="<f4").tobytes()
                    ).decode("ascii"),
                }
                for model_id, values in self.vectors.items()
            ],
            "phash": self.hashes.phash,
            "dhash": self.hashes.dhash,
            "width": self.width,
            "height": self.height,
            "thumb_base64": base64.b64encode(self.thumb).decode("ascii") if self.thumb else "",
        }


@dataclass
class WorkerInfo:
    device: str
    batch_size: int
    specs: list[ModelSpec] = field(default_factory=list)

    def to_payload(self) -> dict:
        return {
            "device": self.device,
            "batch_size": self.batch_size,
            "models": [spec.as_dict(self.device) for spec in self.specs],
        }
