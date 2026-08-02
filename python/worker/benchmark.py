"""모델 비교 측정.

"이 그림의 원본을 찾는다"가 실제 목적이므로, 망가뜨린 복사본으로 검색해서
원본이 1등으로 나오는 비율을 잽니다. 인터넷의 벤치마크는 대부분 실사 사진
기준이라 애니 그림에서 어떨지는 직접 재봐야 합니다.

사용법:
    .venv/bin/python benchmark.py --images ~/.saucedust/thumbs --limit 300
    .venv/bin/python benchmark.py --images ./samples --models bench_models.json
"""

from __future__ import annotations

import argparse
import io
import json
import random
import time
from dataclasses import dataclass
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

import backends
import device
from domain import ModelSpec, hashing, imaging, parse_specs
from ports import Encoder

IMAGE_SUFFIXES = {".jpg", ".jpeg", ".png", ".webp"}

CANDIDATES = [
    {"id": "dinov2-vitb14", "kind": "copy", "backend": "transformers",
     "checkpoint": "facebook/dinov2-base", "vector_size": 768, "input_size": 224},
    {"id": "siglip-b16", "kind": "semantic", "backend": "transformers",
     "checkpoint": "google/siglip-base-patch16-224", "vector_size": 768, "input_size": 224},
    {"id": "clip-b32", "kind": "semantic", "backend": "transformers",
     "checkpoint": "laion/CLIP-ViT-B-32-laion2B-s34B-b79K", "vector_size": 512,
     "input_size": 224},
]


# 사람들이 실제로 올리는 형태를 흉내 냅니다.

def degrade_resize(image: Image.Image) -> Image.Image:
    w, h = image.size
    return image.resize((max(1, w // 2), max(1, h // 2)), Image.Resampling.LANCZOS)


def degrade_jpeg(image: Image.Image) -> Image.Image:
    buffer = io.BytesIO()
    image.save(buffer, format="JPEG", quality=40)
    buffer.seek(0)
    return Image.open(buffer).convert("RGB")


def degrade_crop(image: Image.Image) -> Image.Image:
    w, h = image.size
    dx, dy = int(w * 0.1), int(h * 0.1)
    return image.crop((dx, dy, w - dx, h - dy))


def degrade_watermark(image: Image.Image) -> Image.Image:
    copy = image.copy()
    draw = ImageDraw.Draw(copy)
    w, h = copy.size
    draw.rectangle([(0, int(h * 0.88)), (w, h)], fill=(20, 20, 20))
    draw.text((int(w * 0.04), int(h * 0.90)), "SAMPLE", fill=(240, 240, 240))
    return copy


def degrade_screenshot(image: Image.Image) -> Image.Image:
    """화면을 찍어 여백이 붙은 형태를 흉내 냅니다."""
    w, h = image.size
    pad = int(min(w, h) * 0.08)
    canvas = Image.new("RGB", (w + pad * 2, h + pad * 2), (18, 18, 22))
    canvas.paste(image, (pad, pad))
    return canvas


DEGRADATIONS = {
    "절반 축소": degrade_resize,
    "JPEG 40": degrade_jpeg,
    "10% 잘라냄": degrade_crop,
    "워터마크": degrade_watermark,
    "스크린샷": degrade_screenshot,
}


@dataclass
class Score:
    top1: int = 0
    top5: int = 0
    total: int = 0

    def add(self, rank: int) -> None:
        self.total += 1
        if rank == 0:
            self.top1 += 1
        if rank < 5:
            self.top5 += 1

    def pct(self, hits: int) -> float:
        return 100 * hits / self.total if self.total else 0.0


def load_images(folder: Path, limit: int) -> list[Image.Image]:
    paths = [p for p in sorted(folder.rglob("*")) if p.suffix.lower() in IMAGE_SUFFIXES]
    if not paths:
        raise SystemExit(f"{folder} 아래에 이미지가 없습니다")

    random.Random(1234).shuffle(paths)
    out: list[Image.Image] = []
    for path in paths:
        if len(out) >= limit:
            break
        try:
            with Image.open(path) as handle:
                out.append(handle.convert("RGB"))
        except Exception as exc:
            # 측정용 표본이므로 열리지 않는 파일은 건너뜁니다.
            print(f"  건너뜀 {path.name}: {exc}")
            continue
    if len(out) < 10:
        raise SystemExit(f"쓸 수 있는 이미지가 {len(out)}장뿐입니다")
    return out


def _derive(image: Image.Image) -> imaging.Derived:
    decoded = imaging.Decoded(image=image, width=image.width, height=image.height)
    return imaging.derive(decoded)


def prepared(images: list[Image.Image]) -> list[Image.Image]:
    """실제 워커와 같은 준비 과정을 거칩니다. 그래야 측정이 운영과 일치합니다."""
    return [_derive(image).prep for image in images]


def encode_all(encoder: Encoder, images: list[Image.Image], batch: int) -> np.ndarray:
    chunks = [encoder.encode(images[i:i + batch]) for i in range(0, len(images), batch)]
    return np.vstack(chunks)


def rank_of_original(gallery: np.ndarray, query: np.ndarray, index: int) -> int:
    """정답이 몇 등으로 나왔는지 돌려줍니다. 0이면 1등입니다."""
    order = np.argsort(-(gallery @ query))
    return int(np.where(order == index)[0][0])


def evaluate_model(spec: ModelSpec, originals: list[Image.Image],
                   chosen: device.Device, batch: int) -> dict[str, Score]:
    encoder = backends.build(spec, chosen.name, chosen.use_half)
    gallery = encode_all(encoder, prepared(originals), batch)

    results: dict[str, Score] = {}
    for name, transform in DEGRADATIONS.items():
        score = Score()
        queries = encode_all(encoder, prepared([transform(i) for i in originals]), batch)
        for i in range(len(originals)):
            score.add(rank_of_original(gallery, queries[i], i))
        results[name] = score
    return results


def evaluate_phash(originals: list[Image.Image]) -> dict[str, Score]:
    """비교 기준선. 지각 해시만으로 얼마나 찾는지 봅니다."""
    def digest(image: Image.Image) -> int:
        return int(hashing.compute(_derive(image).hash_src).phash, 16)

    gallery = [digest(image) for image in originals]

    results: dict[str, Score] = {}
    for name, transform in DEGRADATIONS.items():
        score = Score()
        for i, image in enumerate(originals):
            query = digest(transform(image))
            distances = [(query ^ candidate).bit_count() for candidate in gallery]
            score.add(int(np.where(np.argsort(distances) == i)[0][0]))
        results[name] = score
    return results


def print_table(title: str, results: dict[str, Score], elapsed: float) -> None:
    print(f"\n{title}  ({elapsed:.1f}초)")
    print(f"  {'망가뜨린 방식':<14} {'1등':>7} {'5등 안':>8}")
    print("  " + "-" * 32)

    total = Score()
    for name, score in results.items():
        print(f"  {name:<14} {score.pct(score.top1):6.1f}% {score.pct(score.top5):7.1f}%")
        total.top1 += score.top1
        total.top5 += score.top5
        total.total += score.total
    print("  " + "-" * 32)
    print(f"  {'전체':<14} {total.pct(total.top1):6.1f}% {total.pct(total.top5):7.1f}%")


def main() -> None:
    parser = argparse.ArgumentParser(description="원본 찾기 성능으로 모델을 비교합니다")
    parser.add_argument("--images", required=True, type=Path, help="이미지가 있는 폴더")
    parser.add_argument("--limit", type=int, default=200, help="쓸 이미지 수")
    parser.add_argument("--models", type=Path, help="후보 모델 JSON 경로")
    parser.add_argument("--batch", type=int, default=0, help="배치 크기")
    args = parser.parse_args()

    imaging.configure_pillow()

    raw = json.loads(args.models.read_text(encoding="utf-8")) if args.models else CANDIDATES
    specs = parse_specs(raw)

    chosen = device.select()
    batch = args.batch or chosen.batch_size

    originals = load_images(args.images, args.limit)
    print(f"장치 {chosen.name}, 이미지 {len(originals)}장, 배치 {batch}")
    print("망가뜨린 복사본으로 검색해서 원본이 몇 등에 나오는지 잽니다.")

    started = time.time()
    print_table("지각 해시만 (기준선)", evaluate_phash(originals), time.time() - started)

    for spec in specs:
        started = time.time()
        try:
            results = evaluate_model(spec, originals, chosen, batch)
        except Exception as exc:
            print(f"\n{spec.id}: 측정하지 못했습니다 ({exc})")
            continue
        print_table(f"{spec.id}  [{spec.checkpoint}]", results, time.time() - started)

    print("\n1등 비율이 가장 높은 모델을 kind=copy로 등록하십시오.")
    print("  saucedust model add -id <이름> -kind copy -vector-size <차원>")


if __name__ == "__main__":
    main()
