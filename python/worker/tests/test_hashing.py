"""지각 해시 검증.

검색 재정렬이 이 해시에 기대므로, 같은 그림은 같게 보고 다른 그림은 다르게
봐야 합니다.
"""

from __future__ import annotations

import io

import numpy as np
from PIL import Image

from domain import hashing, imaging


def hamming(a: str, b: str) -> int:
    assert len(a) == len(b)
    return (int(a, 16) ^ int(b, 16)).bit_count()


def make_image(seed: int, size: tuple[int, int] = (512, 512)) -> Image.Image:
    rng = np.random.default_rng(seed)
    base = rng.random((16, 16, 3)) * 255
    small = Image.fromarray(base.astype(np.uint8), mode="RGB")
    return small.resize(size, Image.Resampling.BICUBIC)


def derive_of(image: Image.Image) -> imaging.Derived:
    decoded = imaging.Decoded(image=image, width=image.width, height=image.height)
    return imaging.derive(decoded)


def hashes_of(image: Image.Image):
    return hashing.compute(derive_of(image).hash_src)


def recompress(image: Image.Image, quality: int) -> Image.Image:
    buffer = io.BytesIO()
    image.convert("RGB").save(buffer, format="JPEG", quality=quality)
    buffer.seek(0)
    return Image.open(buffer).convert("RGB")


def test_hash_length_is_64_bits():
    got = hashes_of(make_image(1))
    assert len(got.phash) == 16
    assert len(got.dhash) == 16


def test_same_image_gives_same_hash():
    image = make_image(2)
    assert hashes_of(image).phash == hashes_of(image.copy()).phash
    assert hashes_of(image).dhash == hashes_of(image.copy()).dhash


def test_resize_keeps_hash_close():
    """크기만 다른 재업로드본은 같은 그림으로 잡혀야 합니다."""
    original = make_image(3, (800, 800))
    resized = original.resize((320, 320), Image.Resampling.LANCZOS)

    assert hamming(hashes_of(original).phash, hashes_of(resized).phash) <= 5
    assert hamming(hashes_of(original).dhash, hashes_of(resized).dhash) <= 8


def test_recompression_keeps_hash_close():
    """JPEG 품질을 크게 낮춰도 같은 그림으로 잡혀야 합니다."""
    original = make_image(4, (600, 600))
    degraded = recompress(original, quality=45)

    assert hamming(hashes_of(original).phash, hashes_of(degraded).phash) <= 5


def test_different_images_differ():
    """서로 다른 그림은 확실히 멀어야 합니다."""
    distances = [
        hamming(hashes_of(make_image(seed)).phash, hashes_of(make_image(seed + 100)).phash)
        for seed in range(10, 20)
    ]
    assert min(distances) >= 12, f"가장 가까운 쌍이 {min(distances)}비트 차이입니다"


def test_brightness_shift_tolerated_by_phash():
    """전체 밝기만 바뀐 그림은 pHash가 같게 봐야 합니다.

    첫 DCT 계수를 기준값에서 뺀 이유가 이것입니다.
    """
    original = make_image(5, (400, 400))
    array = np.asarray(original, dtype=np.int16)
    brighter = Image.fromarray(np.clip(array + 25, 0, 255).astype(np.uint8), mode="RGB")

    assert hamming(hashes_of(original).phash, hashes_of(brighter).phash) <= 3


def test_grayscale_input_is_handled():
    got = hashes_of(make_image(8).convert("L"))
    assert len(got.phash) == 16
    assert len(got.dhash) == 16


# 해시 원천이 고정 크기라 축소본 설정을 바꿔도 해시가 흔들리면 안 됩니다.
def test_hash_is_independent_of_thumbnail_settings():
    image = make_image(11, (1500, 1500))
    derived = derive_of(image)

    before = hashing.compute(derived.hash_src)
    imaging.thumbnail(derived, 256, 60)
    after = hashing.compute(derived.hash_src)

    assert before.phash == after.phash
    assert before.dhash == after.dhash
