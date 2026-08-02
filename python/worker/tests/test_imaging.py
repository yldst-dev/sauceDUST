"""이미지 준비 검증.

가장 비싼 것은 디코드이고, 그다음이 원본 크기에서의 축소입니다.
파생물끼리 서로 영향을 주지 않는지도 함께 확인합니다.
"""

from __future__ import annotations

import io

import numpy as np
import pytest
from PIL import Image

from domain import imaging


def make_image(seed: int, size: tuple[int, int] = (1600, 1200)) -> Image.Image:
    rng = np.random.default_rng(seed)
    base = rng.random((16, 16, 3)) * 255
    small = Image.fromarray(base.astype(np.uint8), mode="RGB")
    return small.resize(size, Image.Resampling.BICUBIC)


def encode(image: Image.Image, fmt: str = "JPEG", **kwargs) -> bytes:
    buffer = io.BytesIO()
    image.save(buffer, format=fmt, **kwargs)
    return buffer.getvalue()


def derive_of(image: Image.Image) -> imaging.Derived:
    """PIL 이미지에서 바로 작업본을 만듭니다."""
    decoded = imaging.Decoded(image=image, width=image.width, height=image.height)
    return imaging.derive(decoded)


def test_decode_rejects_empty():
    with pytest.raises(imaging.ImageError):
        imaging.decode(b"")


def test_decode_rejects_garbage():
    with pytest.raises(imaging.ImageError):
        imaging.decode(b"this is not an image at all")


def test_decode_converts_to_rgb():
    gray = make_image(1, (400, 400)).convert("L")
    assert imaging.decode(encode(gray)).image.mode == "RGB"


# draft로 작게 받아도 원본 크기는 그대로 보고해야 합니다.
# 이 값이 틀리면 검색 결과에 잘못된 해상도가 표시됩니다.
def test_decode_reports_original_size_despite_draft():
    original = make_image(2, (3000, 2000))
    decoded = imaging.decode(encode(original, quality=90))

    assert (decoded.width, decoded.height) == (3000, 2000)
    # JPEG이면 디코더가 미리 줄여 주므로 실제 화소는 더 작아도 됩니다.
    assert max(decoded.image.size) <= 3000


def test_decode_handles_png_without_draft():
    """PNG에는 draft가 없습니다. 그대로 나와야 합니다."""
    decoded = imaging.decode(encode(make_image(3, (800, 600)), fmt="PNG"))

    assert (decoded.width, decoded.height) == (800, 600)
    assert decoded.image.size == (800, 600)


def test_derive_keeps_original_dimensions():
    derived = derive_of(make_image(4, (1600, 1200)))

    assert derived.width == 1600
    assert derived.height == 1200
    assert max(derived.prep.size) == imaging.PREP_SIZE


def test_derive_preserves_aspect_ratio():
    derived = derive_of(make_image(5, (2000, 1000)))
    assert derived.prep.size == (imaging.PREP_SIZE, imaging.PREP_SIZE // 2)


def test_derive_does_not_upscale_small_images():
    """작은 이미지를 억지로 키우면 없던 정보를 만들어 냅니다."""
    derived = derive_of(make_image(6, (200, 150)))
    assert derived.prep.size == (200, 150)


def test_hash_source_is_grayscale_and_fixed():
    """해시 원천은 모델이나 축소본 설정과 무관하게 고정 크기여야 합니다."""
    derived = derive_of(make_image(7, (3000, 3000)))

    assert derived.hash_src.mode == "L"
    assert max(derived.hash_src.size) == imaging.HASH_SIZE


def test_thumbnail_size_and_format():
    data = imaging.thumbnail(derive_of(make_image(8, (2400, 1600))), 384, 85)

    restored = Image.open(io.BytesIO(data))
    assert restored.format == "JPEG"
    assert max(restored.size) == 384
    # 1,000만 장을 보관해도 감당할 수 있어야 합니다.
    assert len(data) < 120_000


def test_thumbnail_does_not_upscale_beyond_prep():
    """축소본 설정을 작업본보다 크게 잡아도 억지로 키우지 않아야 합니다."""
    derived = derive_of(make_image(9, (2000, 2000)))
    restored = Image.open(io.BytesIO(imaging.thumbnail(derived, 4000, 85)))

    assert max(restored.size) == imaging.PREP_SIZE


# 작업본을 만든 뒤로는 원본 크기에서 다시 축소하지 않아야 합니다.
def test_no_extra_resampling_after_prep(monkeypatch):
    original = make_image(10, (2400, 1800))
    calls: list[tuple[int, int]] = []
    real_resize = Image.Image.resize

    def counting_resize(self, size, *args, **kwargs):
        calls.append(self.size)
        return real_resize(self, size, *args, **kwargs)

    monkeypatch.setattr(Image.Image, "resize", counting_resize)

    derived = derive_of(original)
    imaging.thumbnail(derived, 384, 85)

    from_original = [size for size in calls if size == (2400, 1800)]
    assert len(from_original) == 1, f"원본에서 {len(from_original)}번 축소했습니다: {calls}"


def test_prepare_is_deterministic():
    """같은 입력은 항상 같은 결과여야 합니다. 벡터가 흔들리면 안 됩니다."""
    raw = encode(make_image(11, (1200, 900)), quality=90)

    first = imaging.thumbnail(imaging.prepare(raw), 384, 85)
    second = imaging.thumbnail(imaging.prepare(raw), 384, 85)
    assert first == second


def test_prepare_end_to_end():
    raw = encode(make_image(12, (2000, 1500)), quality=88)
    derived = imaging.prepare(raw)

    assert (derived.width, derived.height) == (2000, 1500)
    assert max(derived.prep.size) == imaging.PREP_SIZE
    assert derived.hash_src.mode == "L"


# 투명한 곳을 채우지 않으면 인코더가 남긴 값이 그대로 드러납니다.
# 같은 그림을 다른 도구로 저장했을 때 다른 벡터가 나오게 됩니다.
def test_flatten_fills_transparency_with_white():
    from PIL import Image

    from domain.imaging import WHITE, flatten

    rgba = Image.new("RGBA", (8, 8), (0, 0, 0, 0))
    rgba.putpixel((4, 4), (10, 20, 30, 255))

    out = flatten(rgba)
    assert out.mode == "RGB"
    assert out.getpixel((0, 0)) == WHITE
    assert out.getpixel((4, 4)) == (10, 20, 30)


def test_flatten_leaves_opaque_images_alone():
    from PIL import Image

    from domain.imaging import flatten

    rgb = Image.new("RGB", (4, 4), (7, 8, 9))
    assert flatten(rgb).getpixel((0, 0)) == (7, 8, 9)

    # 알파가 있어도 전부 불투명하면 그대로여야 합니다.
    opaque = Image.new("RGBA", (4, 4), (7, 8, 9, 255))
    assert flatten(opaque).getpixel((0, 0)) == (7, 8, 9)


def test_flatten_handles_palette_transparency():
    from PIL import Image

    from domain.imaging import WHITE, flatten

    palette = Image.new("P", (4, 4), 0)
    palette.putpalette([0, 0, 0] + [255, 0, 0] * 255)
    palette.info["transparency"] = 0

    out = flatten(palette)
    assert out.mode == "RGB"
    assert out.getpixel((0, 0)) == WHITE


def test_flatten_handles_grayscale_alpha():
    from PIL import Image

    from domain.imaging import WHITE, flatten

    la = Image.new("LA", (4, 4), (0, 0))
    assert flatten(la).getpixel((0, 0)) == WHITE


# 같은 그림이면 알파 아래에 무엇이 들어 있든 같은 해시가 나와야 합니다.
def test_transparent_pixels_do_not_change_the_hash():
    from PIL import Image

    from domain import hashing
    from domain.imaging import Decoded, derive

    def digest(image):
        d = derive(Decoded(image=image, width=image.width, height=image.height))
        return int(hashing.compute(d.hash_src).phash, 16)

    from domain.imaging import flatten

    base = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
    for x in range(20, 44):
        for y in range(20, 44):
            base.putpixel((x, y), (200, 30, 60, 255))

    # 투명한 자리에 서로 다른 값을 넣습니다. 보이지 않으므로 결과가 같아야 합니다.
    noisy = base.copy()
    alpha = base.split()[-1]
    for x in range(64):
        for y in range(64):
            if alpha.getpixel((x, y)) == 0:
                noisy.putpixel((x, y), (x * 3 % 256, y * 5 % 256, 99, 0))

    assert digest(flatten(base)) == digest(flatten(noisy))
