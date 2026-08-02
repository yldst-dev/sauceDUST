"""지각 해시.

이미 줄여 둔 작업본을 받습니다. 원본에서 다시 축소하지 않으므로 비용이 거의 없습니다.
"""

from __future__ import annotations

import numpy as np
from PIL import Image

from .spec import Hashes

_PHASH_SIZE = 32
_PHASH_LOW = 8


def _dct_matrix(n: int) -> np.ndarray:
    k = np.arange(n)
    matrix = np.cos(np.pi * (2 * k[None, :] + 1) * k[:, None] / (2 * n))
    matrix[0] *= 1 / np.sqrt(2)
    return matrix * np.sqrt(2 / n)


_DCT = _dct_matrix(_PHASH_SIZE)


def _bits_to_hex(bits: np.ndarray) -> str:
    return np.packbits(bits.astype(np.uint8)).tobytes().hex()


def _gray(image: Image.Image) -> Image.Image:
    return image if image.mode == "L" else image.convert("L")


def phash(image: Image.Image) -> str:
    """주파수 영역 해시. 크기 변경과 재압축에 강합니다."""
    small = _gray(image).resize((_PHASH_SIZE, _PHASH_SIZE), Image.Resampling.LANCZOS)
    coeffs = _DCT @ np.asarray(small, dtype=np.float64) @ _DCT.T
    low = coeffs[:_PHASH_LOW, :_PHASH_LOW].flatten()

    # 첫 계수는 전체 밝기라 그림 내용과 무관하므로 기준값 계산에서 뺍니다.
    return _bits_to_hex(low > np.median(low[1:]))


def dhash(image: Image.Image) -> str:
    """이웃 화소 밝기 차이 해시. 계산이 싸고 pHash와 실패 양상이 다릅니다."""
    small = _gray(image).resize((9, 8), Image.Resampling.LANCZOS)
    pixels = np.asarray(small, dtype=np.int16)
    return _bits_to_hex((pixels[:, 1:] > pixels[:, :-1]).flatten())


def compute(image: Image.Image) -> Hashes:
    """회색조 변환을 한 번만 하고 두 해시를 함께 냅니다."""
    gray = _gray(image)
    return Hashes(phash=phash(gray), dhash=dhash(gray))
