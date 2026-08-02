"""DB에 없는 그림을 없다고 말할 수 있는지 잽니다.

benchmark.py는 "정답이 몇 등인가"만 봅니다. 후보 안에 정답이 반드시 있다고
치고 재는 것이라, 정답이 없을 때 무슨 일이 벌어지는지는 알려주지 않습니다.

실제 서비스에서는 이쪽이 더 중요합니다. 사람들은 아직 수집하지 않은 그림도
올립니다. 그때 "없습니다"라고 해야지, 엉뚱한 그림을 자신 있게 내놓으면
안 됩니다. 이걸 하려면 점수에 선을 그을 수 있어야 합니다.

그래서 두 무리의 점수 분포를 봅니다.
  진짜  후보 안에 있는 그림을 망가뜨려 넣었을 때 1등의 점수
  가짜  후보에 없는 그림을 넣었을 때 1등의 점수

둘이 겹치면 어떤 선을 그어도 한쪽을 놓칩니다.

사용법:
    .venv/bin/python threshold.py --images ./gallery --gallery 5000 --queries 800
"""

from __future__ import annotations

import argparse
import json
import random
import time
from pathlib import Path

import numpy as np

import backends
import device
from benchmark import (
    CANDIDATES,
    DEGRADATIONS,
    encode_paths,
    hamming_all,
    hash_paths,
    pick_paths,
)
from domain import ModelSpec, imaging, parse_specs


def separation(real: np.ndarray, fake: np.ndarray, target: float) -> tuple[float, float]:
    """정밀도를 target으로 맞추는 선과 그때의 재현율을 냅니다.

    가짜 중에서 선을 넘는 것이 (1 - target)만큼만 되도록 선을 잡고,
    그 선에서 진짜를 몇 퍼센트나 건지는지 봅니다.
    """
    if len(fake) == 0 or len(real) == 0:
        return 0.0, 0.0
    cut = float(np.quantile(fake, target))
    recall = float(np.count_nonzero(real >= cut)) / len(real)
    return cut, recall


def overlap(real: np.ndarray, fake: np.ndarray) -> float:
    """가짜의 최고점을 넘지 못하는 진짜의 비율입니다. 0이면 완전히 갈립니다."""
    if len(fake) == 0 or len(real) == 0:
        return 1.0
    return float(np.count_nonzero(real <= fake.max())) / len(real)


def at_hash_threshold(real: np.ndarray, fake: np.ndarray, bits: int) -> tuple[float, float]:
    """실제로 쓰는 선에서 얼마나 건지고 얼마나 새는지 봅니다.

    64비트 해시에서 5비트 이내면 같은 그림으로 봅니다. 분위수로 거꾸로
    구한 선이 아니라 이 값이 실제 운영 지점입니다.
    """
    cut = -float(bits)
    recall = float(np.count_nonzero(real >= cut)) / len(real)
    leak = float(np.count_nonzero(fake >= cut)) / len(fake)
    return recall, leak


def report(title: str, real: np.ndarray, fake: np.ndarray, elapsed: float) -> None:
    print(f"\n{title}  ({elapsed:.1f}초)")
    print(f"  진짜 {len(real)}건, 가짜 {len(fake)}건")
    print(f"  진짜 점수  중앙값 {np.median(real):.4f}, 하위 1% {np.quantile(real, 0.01):.4f}")
    print(f"  가짜 점수  중앙값 {np.median(fake):.4f}, 상위 1% {np.quantile(fake, 0.99):.4f}")
    print(f"  겹침       {100 * overlap(real, fake):.1f}%  (0%면 선 하나로 완전히 갈립니다)")

    for target in (0.99, 0.999, 1.0):
        cut, recall = separation(real, fake, target)
        label = "가짜를 하나도 안 통과" if target == 1.0 else f"가짜 통과 {100 * (1 - target):.1f}%"
        print(f"  {label:<20} 선 {cut:.4f}  →  진짜 {100 * recall:.1f}% 건짐")

    # 세 모델이 똑같아 보이는 것이 진짜인지 눈금 탓인지 가릅니다.
    print(f"  평균       진짜 {real.mean():.5f}, 가짜 {fake.mean():.5f}, "
          f"차이 {real.mean() - fake.mean():.5f}")

    # 점수가 음수 해밍 거리일 때만 뜻이 있습니다.
    if real.max() <= 0:
        recall, leak = at_hash_threshold(real, fake, 5)
        print(f"  실제 운영 지점 (5비트 이내를 같은 그림으로)  "
              f"진짜 {100 * recall:.1f}% 건짐, 가짜 {100 * leak:.2f}% 샘")


def model_scores(spec: ModelSpec, gallery_paths: list[Path], probe_paths: list[Path],
                 held_out: list[Path], chosen: device.Device,
                 batch: int) -> tuple[np.ndarray, np.ndarray]:
    encoder = backends.build(spec, chosen.name, chosen.use_half)
    gallery = encode_paths(encoder, gallery_paths, batch)

    real: list[float] = []
    fake: list[float] = []
    for transform in DEGRADATIONS.values():
        hits = encode_paths(encoder, probe_paths, batch, transform)
        real.extend((gallery @ hits.T).max(axis=0).tolist())

        misses = encode_paths(encoder, held_out, batch, transform)
        fake.extend((gallery @ misses.T).max(axis=0).tolist())

    return np.array(real), np.array(fake)


def phash_scores(gallery_paths: list[Path], probe_paths: list[Path],
                 held_out: list[Path]) -> tuple[np.ndarray, np.ndarray]:
    """해시는 거리가 작을수록 가깝습니다. 부호를 뒤집어 다른 것과 맞춥니다."""
    gallery = hash_paths(gallery_paths)

    real: list[float] = []
    fake: list[float] = []
    for transform in DEGRADATIONS.values():
        for digests, bucket in ((hash_paths(probe_paths, transform), real),
                                (hash_paths(held_out, transform), fake)):
            for value in digests:
                bucket.append(-float(hamming_all(gallery, value).min()))

    return np.array(real), np.array(fake)


def pipeline_scores(spec: ModelSpec, gallery_paths: list[Path], probe_paths: list[Path],
                    held_out: list[Path], chosen: device.Device, batch: int,
                    topk: int) -> tuple[np.ndarray, np.ndarray]:
    """실제 검색 방식 그대로 잽니다.

    벡터로 후보를 좁힌 다음, 그 안에서 지각 해시 거리가 가장 가까운 것을
    고릅니다. 최종 점수는 그 해시 거리입니다. 벡터 점수만 보는 것과 달리
    "닮았지만 다른 그림"을 해시가 밀어냅니다.

    점수는 부호를 뒤집은 해시 거리입니다. 클수록 가깝습니다.
    """
    encoder = backends.build(spec, chosen.name, chosen.use_half)
    gallery = encode_paths(encoder, gallery_paths, batch)
    gallery_hashes = hash_paths(gallery_paths)

    real: list[float] = []
    fake: list[float] = []
    for transform in DEGRADATIONS.values():
        for paths, bucket in ((probe_paths, real), (held_out, fake)):
            vectors = encode_paths(encoder, paths, batch, transform)
            digests = hash_paths(paths, transform)

            similarity = gallery @ vectors.T
            for slot in range(len(paths)):
                # 벡터가 고른 상위 후보만 봅니다. 실제 Qdrant 검색과 같습니다.
                top = np.argpartition(-similarity[:, slot], topk)[:topk]
                best = int(hamming_all(gallery_hashes[top], digests[slot]).min())
                bucket.append(-float(best))

    return np.array(real), np.array(fake)


def main() -> None:
    parser = argparse.ArgumentParser(
        description="후보에 없는 그림을 없다고 말할 수 있는지 잽니다")
    parser.add_argument("--images", required=True, type=Path)
    parser.add_argument("--gallery", type=int, default=5000, help="후보로 넣을 수")
    parser.add_argument("--queries", type=int, default=500,
                        help="진짜와 가짜 각각 몇 건으로 재볼지")
    parser.add_argument("--models", type=Path)
    parser.add_argument("--batch", type=int, default=0)
    parser.add_argument("--rerank", type=int, default=0,
                        help="벡터로 좁힌 뒤 해시로 다시 세웁니다. 실제 검색 방식입니다")
    args = parser.parse_args()

    imaging.configure_pillow()

    raw = json.loads(args.models.read_text(encoding="utf-8")) if args.models else CANDIDATES
    specs = parse_specs(raw)

    chosen = device.select()
    batch = args.batch or chosen.batch_size

    # 가짜로 쓸 것은 후보에 넣지 않습니다. 넣으면 정답이 있는 셈이 됩니다.
    everything = pick_paths(args.images, args.gallery + args.queries)
    if len(everything) < args.gallery + 50:
        raise SystemExit(f"이미지가 {len(everything)}장뿐입니다. 더 모으십시오")

    gallery_paths = everything[:args.gallery]
    held_out = everything[args.gallery:][:args.queries]
    probe_paths = random.Random(31).sample(gallery_paths, min(args.queries, len(gallery_paths)))

    print(f"장치 {chosen.name}, 배치 {batch}")
    print(f"후보 {len(gallery_paths)}장, 진짜 질의 {len(probe_paths)}장, "
          f"가짜 질의 {len(held_out)}장")
    print("가짜는 후보에 없는 그림입니다. 이것을 없다고 말할 수 있어야 합니다.")

    if args.rerank > 0:
        print(f"벡터로 상위 {args.rerank}개를 좁힌 뒤 지각 해시로 다시 세웁니다.")
    else:
        started = time.time()
        real, fake = phash_scores(gallery_paths, probe_paths, held_out)
        report("지각 해시만 (기준선, 점수는 음수 해밍 거리)", real, fake, time.time() - started)

    for spec in specs:
        started = time.time()
        try:
            if args.rerank > 0:
                real, fake = pipeline_scores(spec, gallery_paths, probe_paths,
                                             held_out, chosen, batch, args.rerank)
                title = f"{spec.id} + 해시 재정렬  (점수는 음수 해밍 거리)"
            else:
                real, fake = model_scores(spec, gallery_paths, probe_paths,
                                          held_out, chosen, batch)
                title = f"{spec.id}  [{spec.checkpoint}]"
        except Exception as exc:
            print(f"\n{spec.id}: 측정하지 못했습니다 ({exc})")
            continue
        report(title, real, fake, time.time() - started)

    print("\n겹침이 작고, 가짜를 막는 선에서 진짜를 많이 건지는 모델이 좋습니다.")


if __name__ == "__main__":
    main()
