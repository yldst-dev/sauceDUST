"""설정 읽기. torch 없이 import할 수 있습니다."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path

from domain import ModelSpec, SpecError, parse_specs

# models.json이 없을 때 쓰는 기본값입니다.
#
# 원본 찾기에 SigLIP을 씁니다. 이름은 글과 그림을 맞추는 모델처럼 들리지만,
# 실측에서 원본 찾기 1등 비율이 가장 높았습니다. 후보 6,299장 기준 99.3퍼센트로
# DINOv2보다 높고 처리도 더 빠릅니다. 특히 잘라낸 그림에서 98.5퍼센트로
# DINOv2의 97.0퍼센트를 앞섭니다. 근거는 README에 있습니다.
DEFAULT_MODELS = [
    {
        "id": "siglip-b16",
        "kind": "copy",
        "backend": "transformers",
        "checkpoint": "google/siglip-base-patch16-224",
        "vector_size": 768,
        "input_size": 224,
    },
]


@dataclass(frozen=True)
class Settings:
    specs: list[ModelSpec]
    thumb_size: int
    thumb_quality: int
    batch_window_ms: int
    batch_size_override: int


def load(env: dict[str, str] | None = None) -> Settings:
    env = env if env is not None else dict(os.environ)

    settings = Settings(
        specs=parse_specs(_raw_models(env)),
        thumb_size=_int(env, "THUMB_SIZE", 384),
        thumb_quality=_int(env, "THUMB_QUALITY", 85),
        batch_window_ms=_int(env, "EMBED_BATCH_TIMEOUT_MS", 50),
        batch_size_override=_int(env, "EMBED_BATCH_SIZE", 0),
    )

    if settings.thumb_size < 64:
        raise SpecError("THUMB_SIZE는 64 이상이어야 합니다")
    if not 1 <= settings.thumb_quality <= 100:
        raise SpecError("THUMB_QUALITY는 1에서 100 사이여야 합니다")
    if settings.batch_window_ms < 0:
        raise SpecError("EMBED_BATCH_TIMEOUT_MS는 0 이상이어야 합니다")
    return settings


def _raw_models(env: dict[str, str]) -> list:
    if path := env.get("SAUCEDUST_MODELS_PATH"):
        return json.loads(Path(path).read_text(encoding="utf-8"))
    if inline := env.get("SAUCEDUST_MODELS"):
        return json.loads(inline)

    default = Path(__file__).with_name("models.json")
    if default.exists():
        return json.loads(default.read_text(encoding="utf-8"))
    return DEFAULT_MODELS


def _int(env: dict[str, str], key: str, fallback: int) -> int:
    raw = env.get(key, "").strip()
    if not raw:
        return fallback
    try:
        return int(raw)
    except ValueError as exc:
        raise SpecError(f"{key}는 정수여야 합니다: {raw!r}") from exc
