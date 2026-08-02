"""설정 읽기. torch 없이 import할 수 있습니다."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path

from domain import ModelSpec, SpecError, parse_specs

DEFAULT_MODELS = [
    {
        "id": "dinov2-vitb14",
        "kind": "copy",
        "backend": "transformers",
        "checkpoint": "facebook/dinov2-base",
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
