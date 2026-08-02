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
    # 같은 계열을 키우면 나아지는지 봅니다. 계산은 세 배쯤 듭니다.
    {"id": "dinov2-vitl14", "kind": "copy", "backend": "transformers",
     "checkpoint": "facebook/dinov2-large", "vector_size": 1024, "input_size": 224},
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


def pick_paths(folder: Path, limit: int) -> list[Path]:
    """쓸 이미지 경로를 고릅니다. 파일은 아직 열지 않습니다.

    후보가 수천 장이 되면 전부 메모리에 올릴 수 없습니다. 8천 장을 512픽셀
    작업본으로 들고 있으면 6기가가 넘습니다. 필요할 때 한 배치씩 읽습니다.
    """
    paths = [p for p in sorted(folder.rglob("*")) if p.suffix.lower() in IMAGE_SUFFIXES]
    if not paths:
        raise SystemExit(f"{folder} 아래에 이미지가 없습니다")

    random.Random(1234).shuffle(paths)
    if len(paths) > limit:
        paths = paths[:limit]
    if len(paths) < 10:
        raise SystemExit(f"쓸 수 있는 이미지가 {len(paths)}장뿐입니다")
    return paths


def _derive(image: Image.Image) -> imaging.Derived:
    decoded = imaging.Decoded(image=image, width=image.width, height=image.height)
    return imaging.derive(decoded)


def _open(path: Path) -> Image.Image | None:
    try:
        with Image.open(path) as handle:
            return handle.convert("RGB")
    except Exception:
        # 측정용 표본이므로 열리지 않는 파일은 건너뜁니다.
        return None


def prepared(images: list[Image.Image]) -> list[Image.Image]:
    """실제 워커와 같은 준비 과정을 거칩니다. 그래야 측정이 운영과 일치합니다."""
    return [_derive(image).prep for image in images]


def encode_paths(encoder: Encoder, paths: list[Path], batch: int,
                 transform=None) -> np.ndarray:
    """경로를 배치 단위로 읽어 인코딩합니다. 읽은 것은 바로 놓아 줍니다."""
    chunks = []
    for start in range(0, len(paths), batch):
        images = []
        for path in paths[start:start + batch]:
            image = _open(path)
            if image is None:
                # 자리를 비우면 색인이 어긋나므로 검은 그림으로 채웁니다.
                image = Image.new("RGB", (64, 64))
            images.append(transform(image) if transform else image)
        chunks.append(encoder.encode(prepared(images)))
    return np.vstack(chunks)


def hash_paths(paths: list[Path], transform=None) -> np.ndarray:
    """지각 해시를 uint64 배열로 냅니다. 배열이라야 한꺼번에 비교할 수 있습니다."""
    out = np.empty(len(paths), dtype=np.uint64)
    for i, path in enumerate(paths):
        image = _open(path) or Image.new("RGB", (64, 64))
        if transform:
            image = transform(image)
        out[i] = np.uint64(int(hashing.compute(_derive(image).hash_src).phash, 16))
    return out


# 64비트 값의 1 개수를 한꺼번에 셉니다. 파이썬 반복으로 8천 x 1천을 돌면
# 몇 분씩 걸립니다. 바이트별 표를 미리 만들어 두면 배열 연산으로 끝납니다.
_POPCOUNT = np.array([bin(i).count("1") for i in range(256)], dtype=np.uint8)


def hamming_all(gallery: np.ndarray, query: np.uint64) -> np.ndarray:
    diff = np.bitwise_xor(gallery, query)
    return _POPCOUNT[diff.view(np.uint8).reshape(-1, 8)].sum(axis=1)


def rank_of(scores: np.ndarray, index: int, higher_is_better: bool) -> int:
    """정답이 몇 등으로 나왔는지 셉니다. 0이면 1등입니다.

    전체를 정렬하지 않고 정답보다 앞선 것만 셉니다. 후보가 많을수록 차이가
    큽니다. 같은 점수는 정답보다 앞선 것으로 봐서 낮게 잡습니다.
    """
    mine = scores[index]
    if higher_is_better:
        better = int(np.count_nonzero(scores > mine))
        tied = int(np.count_nonzero(scores == mine)) - 1
    else:
        better = int(np.count_nonzero(scores < mine))
        tied = int(np.count_nonzero(scores == mine)) - 1
    return better + tied


def evaluate_model(spec: ModelSpec, paths: list[Path], queries: list[int],
                   chosen: device.Device, batch: int) -> dict[str, Score]:
    encoder = backends.build(spec, chosen.name, chosen.use_half)
    gallery = encode_paths(encoder, paths, batch)

    query_paths = [paths[i] for i in queries]
    results: dict[str, Score] = {}
    for name, transform in DEGRADATIONS.items():
        score = Score()
        vectors = encode_paths(encoder, query_paths, batch, transform)
        for slot, index in enumerate(queries):
            score.add(rank_of(gallery @ vectors[slot], index, higher_is_better=True))
        results[name] = score
    return results


def evaluate_phash(paths: list[Path], queries: list[int]) -> dict[str, Score]:
    """비교 기준선. 지각 해시만으로 얼마나 찾는지 봅니다."""
    gallery = hash_paths(paths)
    query_paths = [paths[i] for i in queries]

    results: dict[str, Score] = {}
    for name, transform in DEGRADATIONS.items():
        score = Score()
        digests = hash_paths(query_paths, transform)
        for slot, index in enumerate(queries):
            distances = hamming_all(gallery, digests[slot])
            score.add(rank_of(distances, index, higher_is_better=False))
        results[name] = score
    return results


def print_table(title: str, results: dict[str, Score], elapsed: float,
                encodes: int = 0) -> None:
    # 처리 속도도 함께 봅니다. 정확도가 조금 나은 대신 세 배 느린 모델은
    # 수집 시간을 세 배로 늘립니다. 둘을 같이 봐야 고를 수 있습니다.
    speed = f", {encodes / elapsed:.0f}장/초" if encodes and elapsed > 0 else ""
    print(f"\n{title}  ({elapsed:.1f}초{speed})")
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
    parser.add_argument("--limit", type=int, default=200,
                        help="후보로 삼을 이미지 수. 이 값이 클수록 어려운 문제입니다")
    parser.add_argument("--queries", type=int, default=0,
                        help="그중 몇 장으로 검색해 볼지. 0이면 후보 전부 또는 500장")
    parser.add_argument("--models", type=Path, help="후보 모델 JSON 경로")
    parser.add_argument("--batch", type=int, default=0, help="배치 크기")
    args = parser.parse_args()

    imaging.configure_pillow()

    raw = json.loads(args.models.read_text(encoding="utf-8")) if args.models else CANDIDATES
    specs = parse_specs(raw)

    chosen = device.select()
    batch = args.batch or chosen.batch_size

    paths = pick_paths(args.images, args.limit)

    # 후보 수와 질의 수를 나눕니다. 어려움을 정하는 것은 후보 수이고,
    # 측정에 드는 시간을 정하는 것은 질의 수입니다. 둘을 묶어 두면 후보를
    # 늘릴수록 시간이 제곱으로 불어나 규모를 키울 수가 없습니다.
    count = args.queries or min(500, len(paths))
    count = min(count, len(paths))
    queries = sorted(random.Random(99).sample(range(len(paths)), count))

    print(f"장치 {chosen.name}, 배치 {batch}")
    print(f"후보 {len(paths)}장 중 {count}장으로 검색합니다.")
    print("망가뜨린 복사본으로 검색해서 원본이 몇 등에 나오는지 잽니다.")

    started = time.time()
    print_table("지각 해시만 (기준선)", evaluate_phash(paths, queries),
                time.time() - started)

    encodes = len(paths) + count * len(DEGRADATIONS)
    for spec in specs:
        started = time.time()
        try:
            results = evaluate_model(spec, paths, queries, chosen, batch)
        except Exception as exc:
            print(f"\n{spec.id}: 측정하지 못했습니다 ({exc})")
            continue
        print_table(f"{spec.id}  [{spec.checkpoint} · {spec.vector_size}차원]",
                    results, time.time() - started, encodes)

    print("\n1등 비율이 가장 높은 모델을 kind=copy로 등록하십시오.")
    print("  saucedust model add -id <이름> -kind copy -vector-size <차원>")


if __name__ == "__main__":
    main()
