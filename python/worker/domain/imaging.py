"""이미지 준비.

비싼 것은 원본 크기에서의 리샘플링입니다. 그래서 원본에서 한 번만 줄이고,
해시와 축소본과 모델 입력을 모두 그 결과에서 파생시킵니다.

PREP_SIZE는 고정 상수입니다. 이 값이 바뀌면 벡터와 해시가 전부 달라지므로
바꾸면 다시 계산해야 합니다. 모델 설정이나 축소본 설정과 엮지 않은 이유가
이것입니다. 그쪽을 건드렸다고 벡터가 조용히 달라지면 안 됩니다.
"""

from __future__ import annotations

import io
import warnings
from dataclasses import dataclass

from PIL import Image

# 준비 이미지 크기. 모델 입력(보통 224)보다 넉넉히 크고, 축소본(384)보다도 큽니다.
PREP_SIZE = 512

# 해시 계산용 크기. 모델이나 축소본 설정과 무관하게 고정입니다.
HASH_SIZE = 128

# 압축 폭탄 방어. 정상 이미지는 이 크기를 넘지 않습니다.
MAX_PIXELS = 64_000_000


class ImageError(ValueError):
    """이미지를 쓸 수 없을 때 냅니다."""


@dataclass
class Derived:
    """원본에서 한 번 줄여 만든 작업본과 그 파생물.

    prep은 모델 입력과 축소본의 원천이고, hash_src는 해시 전용입니다.
    width와 height는 원본 크기를 그대로 보존합니다.
    """

    prep: Image.Image
    hash_src: Image.Image
    width: int
    height: int


def configure_pillow() -> None:
    Image.MAX_IMAGE_PIXELS = MAX_PIXELS
    warnings.simplefilter("error", Image.DecompressionBombWarning)


@dataclass
class Decoded:
    """디코드 결과. size는 축소 전 원본 크기입니다."""

    image: Image.Image
    width: int
    height: int


def decode(raw: bytes) -> Decoded:
    """이미지를 엽니다.

    가장 비싼 단계는 축소가 아니라 디코드입니다. JPEG은 디코더에게 미리
    작게 내놓으라고 요청할 수 있습니다(draft). 4000픽셀 JPEG을 512에 맞춰
    받으면 디코드 비용이 크게 줄어듭니다. PNG와 WebP에서는 아무 일도 없습니다.

    draft는 실제 크기를 바꾸므로 원본 크기를 미리 기록해 둡니다.
    """
    if not raw:
        raise ImageError("빈 파일입니다")
    try:
        image = Image.open(io.BytesIO(raw))
        width, height = image.size

        image.draft("RGB", (PREP_SIZE, PREP_SIZE))
        image.load()

        return Decoded(image=image.convert("RGB"), width=width, height=height)
    except Image.DecompressionBombWarning as exc:
        raise ImageError("이미지가 너무 큽니다") from exc
    except ImageError:
        raise
    except Exception as exc:
        raise ImageError(f"이미지를 열지 못했습니다: {exc}") from exc


def derive(decoded: Decoded) -> Derived:
    """이후 단계가 모두 재사용할 작업본을 만듭니다."""
    prep = _fit(decoded.image, PREP_SIZE)
    # 해시용은 이미 작아진 prep에서 다시 줄이므로 거의 공짜입니다.
    hash_src = _fit(prep, HASH_SIZE).convert("L")

    return Derived(prep=prep, hash_src=hash_src,
                   width=decoded.width, height=decoded.height)


def prepare(raw: bytes) -> Derived:
    """디코드부터 작업본까지 한 번에 처리합니다."""
    return derive(decode(raw))


def thumbnail(derived: Derived, size: int, quality: int) -> bytes:
    """모델 교체 시 다시 계산할 때 쓸 축소본입니다.

    원본은 저장하지 않습니다. 긴 변을 size에 맞추므로 1,000만 장을 보관해도
    수백 기가 수준이며, 나중에 모델을 바꿔도 다시 내려받지 않아도 됩니다.
    """
    source = _fit(derived.prep, size)

    buffer = io.BytesIO()
    source.save(buffer, format="JPEG", quality=quality, optimize=True)
    return buffer.getvalue()


def _fit(image: Image.Image, size: int) -> Image.Image:
    """긴 변을 size에 맞춰 줄입니다. 이미 작으면 그대로 둡니다.

    LANCZOS를 씁니다. BILINEAR이 2.7배 빠르지만 2x2 이웃만 보므로 3배 축소에서
    선화에 계단이 생길 수 있습니다. 이 단계는 전체 처리 시간의 일부일 뿐이고
    다운로드가 훨씬 오래 걸리므로, 벡터 품질을 걸고 몇 밀리초를 아끼지 않습니다.
    reducing_gap도 재봤지만 이 축소 비율에서는 차이가 없어 넣지 않았습니다.
    """
    longest = max(image.size)
    if longest <= size:
        return image

    scale = size / longest
    target = (max(1, round(image.width * scale)), max(1, round(image.height * scale)))
    return image.resize(target, Image.Resampling.LANCZOS)
