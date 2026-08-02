"""장치 선택.

CUDA, MPS, CPU 순으로 고릅니다. CUDA에서는 fp16을 쓰고 배치를 크게 잡습니다.
"""

from __future__ import annotations

from dataclasses import dataclass

# 장치별 감당할 만한 배치 크기입니다. GPU는 한 장씩 넣으면 대부분 놀게 됩니다.
BATCH_SIZES = {"cuda": 64, "mps": 16, "cpu": 4}


@dataclass(frozen=True)
class Device:
    name: str
    use_half: bool

    @property
    def batch_size(self) -> int:
        return BATCH_SIZES.get(self.name, 4)


def select() -> Device:
    import torch

    if torch.cuda.is_available():
        return Device(name="cuda", use_half=True)
    if torch.backends.mps.is_available() and torch.backends.mps.is_built():
        return Device(name="mps", use_half=False)
    return Device(name="cpu", use_half=False)


CPU = Device(name="cpu", use_half=False)
