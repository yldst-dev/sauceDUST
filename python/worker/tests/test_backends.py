"""모델 출력에서 벡터를 꺼내는 부분을 시험합니다.

실제 모델을 올리지 않고 출력 형태만 흉내 냅니다. 가중치를 받는 데 몇 기가가
들고 몇 분이 걸리므로, 그것 없이 확인할 수 있는 것은 그렇게 합니다.
"""

from __future__ import annotations

from dataclasses import dataclass

import pytest
import torch

from backends import BackendError, as_vectors


@dataclass
class Wrapped:
    """transformers가 돌려주는 출력 객체 흉내입니다."""

    last_hidden_state: torch.Tensor | None = None
    pooler_output: torch.Tensor | None = None


def test_tensor_passes_through():
    tensor = torch.zeros(4, 768)
    assert as_vectors(tensor) is tensor


# transformers 5.x에서 CLIP과 SigLIP의 get_image_features가 텐서 대신
# 객체를 돌려주도록 바뀌었습니다. 그 탓에 semantic 모델이 통째로 죽었습니다.
# 모델을 올릴 때는 멀쩡하고 이미지를 넣는 순간에야 터져서 알아채기 어렵습니다.
def test_wrapped_output_uses_pooler():
    out = Wrapped(
        last_hidden_state=torch.zeros(4, 50, 768),
        pooler_output=torch.ones(4, 512),
    )
    got = as_vectors(out)
    assert got.shape == (4, 512)
    assert torch.all(got == 1)


def test_falls_back_to_first_token():
    hidden = torch.zeros(2, 10, 768)
    hidden[:, 0] = 7
    got = as_vectors(Wrapped(last_hidden_state=hidden))
    assert got.shape == (2, 768)
    assert torch.all(got == 7)


def test_pooler_of_none_is_skipped():
    hidden = torch.zeros(2, 10, 768)
    hidden[:, 0] = 3
    got = as_vectors(Wrapped(last_hidden_state=hidden, pooler_output=None))
    assert got.shape == (2, 768)
    assert torch.all(got == 3)


def test_unknown_output_is_rejected():
    # 조용히 넘어가면 차원이 어긋난 벡터가 저장됩니다.
    with pytest.raises(BackendError, match="벡터를 찾지 못했습니다"):
        as_vectors(object())
    with pytest.raises(BackendError):
        as_vectors(Wrapped())


@pytest.mark.parametrize(
    "checkpoint,dim",
    [
        ("laion/CLIP-ViT-B-32-laion2B-s34B-b79K", 512),
        ("google/siglip-base-patch16-224", 768),
        ("facebook/dinov2-base", 768),
    ],
)
def test_real_model_dimensions(checkpoint, dim):
    """실제 모델이 설정한 차원을 내는지 봅니다.

    가중치를 받아야 하므로 평소에는 건너뜁니다. transformers를 올린 뒤에는
    한 번 돌려서 출력 형태가 그대로인지 확인하십시오.

        SAUCEDUST_TEST_MODELS=1 .venv/bin/python -m pytest tests/test_backends.py
    """
    import os

    if not os.getenv("SAUCEDUST_TEST_MODELS"):
        pytest.skip("가중치를 받아야 합니다. SAUCEDUST_TEST_MODELS=1을 넣으십시오")

    from transformers import AutoModel

    model = AutoModel.from_pretrained(checkpoint).eval()
    pixels = torch.zeros(1, 3, 224, 224)

    with torch.inference_mode():
        if hasattr(model, "get_image_features") and hasattr(model, "vision_model"):
            out = model.get_image_features(pixel_values=pixels)
        else:
            out = model(pixels)
        vectors = as_vectors(out)

    assert vectors.shape == (1, dim), (
        f"{checkpoint}가 {tuple(vectors.shape)}을 냈습니다. "
        f"models.json의 vector_size를 {vectors.shape[1]}로 고치십시오"
    )
    # float()를 부를 수 있어야 encode가 돕니다.
    assert vectors.float().shape == (1, dim)
