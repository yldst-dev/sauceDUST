"""설정 검증. 잘못된 설정은 시작할 때 막아야 합니다."""

from __future__ import annotations

import json

import pytest

import config
from domain import SpecError, parse_specs

VALID = {
    "id": "dinov2-vitb14", "kind": "copy", "backend": "transformers",
    "checkpoint": "facebook/dinov2-base", "vector_size": 768, "input_size": 224,
}


def test_parse_valid_spec():
    spec = parse_specs([VALID])[0]

    assert spec.id == "dinov2-vitb14"
    assert spec.kind == "copy"
    assert spec.vector_size == 768
    assert spec.input_size == 224


def test_input_size_defaults():
    raw = {k: v for k, v in VALID.items() if k != "input_size"}
    assert parse_specs([raw])[0].input_size == 224


@pytest.mark.parametrize("change,reason", [
    ({"kind": "무언가"}, "모르는 용도"),
    ({"vector_size": 0}, "차원 0"),
    ({"vector_size": -5}, "음수 차원"),
    ({"input_size": 0}, "입력 크기 0"),
])
def test_rejects_bad_values(change, reason):
    with pytest.raises(SpecError):
        parse_specs([{**VALID, **change}])


@pytest.mark.parametrize("missing", ["id", "kind", "backend", "checkpoint", "vector_size"])
def test_rejects_missing_fields(missing):
    raw = {k: v for k, v in VALID.items() if k != missing}
    with pytest.raises(SpecError):
        parse_specs([raw])


def test_rejects_empty_list():
    with pytest.raises(SpecError):
        parse_specs([])


# 같은 식별자가 둘이면 나중 것이 앞의 것을 덮어써 벡터가 섞입니다.
def test_rejects_duplicate_ids():
    with pytest.raises(SpecError):
        parse_specs([VALID, {**VALID, "checkpoint": "다른/것"}])


def test_load_from_inline_env():
    settings = config.load({"SAUCEDUST_MODELS": json.dumps([VALID])})

    assert len(settings.specs) == 1
    assert settings.thumb_size == 384
    assert settings.thumb_quality == 85


def test_load_from_file(tmp_path):
    path = tmp_path / "models.json"
    path.write_text(json.dumps([VALID]), encoding="utf-8")

    settings = config.load({"SAUCEDUST_MODELS_PATH": str(path)})
    assert settings.specs[0].id == "dinov2-vitb14"


def test_load_reads_numeric_env():
    settings = config.load({
        "SAUCEDUST_MODELS": json.dumps([VALID]),
        "THUMB_SIZE": "256",
        "THUMB_QUALITY": "70",
        "EMBED_BATCH_TIMEOUT_MS": "20",
        "EMBED_BATCH_SIZE": "32",
    })

    assert settings.thumb_size == 256
    assert settings.thumb_quality == 70
    assert settings.batch_window_ms == 20
    assert settings.batch_size_override == 32


@pytest.mark.parametrize("env,reason", [
    ({"THUMB_SIZE": "10"}, "너무 작은 축소본"),
    ({"THUMB_QUALITY": "0"}, "품질 0"),
    ({"THUMB_QUALITY": "200"}, "품질 초과"),
    ({"THUMB_SIZE": "숫자아님"}, "숫자가 아님"),
])
def test_load_rejects_bad_env(env, reason):
    with pytest.raises(SpecError):
        config.load({"SAUCEDUST_MODELS": json.dumps([VALID]), **env})


# 모델 선택은 되돌리기 비싼 결정입니다. 바꾸면 쌓인 벡터를 전부 다시
# 계산해야 하므로, 실측으로 정한 값이 실수로 바뀌지 않게 못 박아 둡니다.
# 근거는 README의 모델 비교 실측에 있습니다.
def test_default_copy_model_is_the_measured_winner():
    settings = config.load({})
    copies = [s for s in settings.specs if s.kind == "copy"]

    assert len(copies) == 1, "copy 모델은 하나여야 합니다"
    assert copies[0].id == "siglip-b16"
    assert copies[0].checkpoint == "google/siglip-base-patch16-224"
    assert copies[0].vector_size == 768


def test_shipped_models_json_matches_the_decision():
    from pathlib import Path

    path = Path(__file__).resolve().parent.parent / "models.json"
    specs = config.load({"SAUCEDUST_MODELS_PATH": str(path)}).specs

    by_kind = {s.kind: s for s in specs}
    assert by_kind["copy"].id == "siglip-b16"
    assert by_kind["copy"].vector_size == 768
    assert by_kind["semantic"].id == "clip-b32"
    assert by_kind["semantic"].vector_size == 512

    # 차원이 어긋나면 Qdrant 컬렉션과 맞지 않아 저장이 통째로 막힙니다.
    for spec in specs:
        assert spec.vector_size > 0
        assert spec.backend == "transformers"
