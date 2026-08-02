"""바깥 세계에 요구하는 계약.

torch 없이 import할 수 있습니다. 유스케이스는 이 모양만 알면 되므로
시험에서는 가짜 구현을 꽂아 무거운 의존성 없이 확인할 수 있습니다.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

import numpy as np
from PIL import Image

from domain import ModelSpec


@runtime_checkable
class Encoder(Protocol):
    """모델 하나를 감쌉니다."""

    @property
    def spec(self) -> ModelSpec: ...

    def encode(self, images: list[Image.Image]) -> np.ndarray:
        """이미지 묶음을 (개수, 차원) 배열로 바꿉니다. 단위 길이로 정규화합니다."""
        ...


class EncoderFactory(Protocol):
    """설정을 받아 실제 모델을 올립니다."""

    def __call__(self, spec: ModelSpec, device: str, use_half: bool) -> Encoder: ...
