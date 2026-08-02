"""모델 백엔드.

새 백엔드를 붙이려면 함수 하나를 만들고 REGISTRY에 등록하면 됩니다.
문자열 분기를 여기저기 흩뿌리지 않기 위한 자리입니다.
"""

from __future__ import annotations

import threading
from collections.abc import Callable

import numpy as np
import torch
from PIL import Image

from domain import ModelSpec


class BackendError(RuntimeError):
    """모델을 올리지 못했을 때 냅니다."""


class TorchEncoder:
    """torch 모델 하나를 감쌉니다.

    전처리는 잠금 밖에서 하고 실제 추론만 직렬화합니다.
    장치가 하나뿐이라 추론은 겹칠 수 없지만, CPU 전처리는 겹칠 수 있습니다.
    """

    def __init__(self, spec: ModelSpec, device: str, use_half: bool,
                 model, preprocess: Callable[[list[Image.Image]], torch.Tensor],
                 features: Callable | None):
        self._spec = spec
        self._device = torch.device(device)
        self._use_half = use_half
        self._model = model
        self._preprocess = preprocess
        self._features = features
        self._lock = threading.Lock()

    @property
    def spec(self) -> ModelSpec:
        return self._spec

    def encode(self, images: list[Image.Image]) -> np.ndarray:
        if not images:
            return np.empty((0, self._spec.vector_size), dtype=np.float32)

        tensor = self._preprocess(images).to(self._device)
        if self._use_half:
            tensor = tensor.half()

        with self._lock, torch.inference_mode():
            out = self._forward(tensor)
            vectors = torch.nn.functional.normalize(out.float(), p=2, dim=-1)
            result = vectors.cpu().numpy()

        if result.shape[1] != self._spec.vector_size:
            raise BackendError(
                f"모델 {self._spec.id}의 벡터 차원이 {result.shape[1]}입니다. "
                f"설정값 {self._spec.vector_size}와 다릅니다"
            )
        return result

    def _forward(self, tensor: torch.Tensor) -> torch.Tensor:
        if self._features is not None:
            return as_vectors(self._features(pixel_values=tensor))
        return as_vectors(self._model(tensor))


def as_vectors(out: object) -> torch.Tensor:
    """모델 출력에서 이미지 한 장당 벡터 하나를 꺼냅니다.

    transformers 판에 따라 텐서를 그대로 주기도 하고 감싼 객체를 주기도 합니다.
    5.x에서 CLIP과 SigLIP의 get_image_features가 텐서 대신 객체를 돌려주도록
    바뀌었고, 그 탓에 semantic 모델이 통째로 동작하지 않았습니다. 모델을 올릴
    때는 아무 문제가 없고 이미지를 넣는 순간에야 터지므로 알아채기 어렵습니다.
    어느 형태가 오든 같게 다룹니다.
    """
    if isinstance(out, torch.Tensor):
        return out

    # pooler_output이 그 모델이 정한 대표값입니다.
    # CLIP은 공용 공간으로 투영한 512차원, SigLIP과 DINOv2는 768차원입니다.
    pooled = getattr(out, "pooler_output", None)
    if pooled is not None:
        return pooled

    hidden = getattr(out, "last_hidden_state", None)
    if hidden is not None:
        # 첫 토큰(CLS)을 이미지 대표값으로 씁니다.
        return hidden[:, 0]

    raise BackendError(
        f"모델 출력에서 벡터를 찾지 못했습니다: {type(out).__name__}. "
        "transformers 판이 바뀌었을 수 있습니다"
    )


def transformers_backend(spec: ModelSpec, device: str, use_half: bool) -> TorchEncoder:
    from transformers import AutoImageProcessor, AutoModel

    processor = AutoImageProcessor.from_pretrained(spec.checkpoint)
    model = AutoModel.from_pretrained(spec.checkpoint)

    # SigLIP과 CLIP은 이미지 탑만 씁니다.
    features = None
    if hasattr(model, "vision_model") and hasattr(model, "get_image_features"):
        features = model.get_image_features

    model = model.to(torch.device(device)).eval()
    if use_half:
        model = model.half()

    def preprocess(images: list[Image.Image]) -> torch.Tensor:
        return processor(images=images, return_tensors="pt")["pixel_values"]

    return TorchEncoder(spec, device, use_half, model, preprocess, features)


def torchscript_backend(spec: ModelSpec, device: str, use_half: bool) -> TorchEncoder:
    from torchvision import transforms

    model = torch.jit.load(spec.checkpoint, map_location=torch.device(device)).eval()
    pipeline = transforms.Compose([
        transforms.Resize((spec.input_size, spec.input_size)),
        transforms.ToTensor(),
        transforms.Normalize(mean=[0.485, 0.456, 0.406], std=[0.229, 0.224, 0.225]),
    ])

    def preprocess(images: list[Image.Image]) -> torch.Tensor:
        return torch.stack([pipeline(image) for image in images])

    return TorchEncoder(spec, device, use_half, model, preprocess, None)


REGISTRY: dict[str, Callable[[ModelSpec, str, bool], TorchEncoder]] = {
    "transformers": transformers_backend,
    "torchscript": torchscript_backend,
}


def build(spec: ModelSpec, device: str, use_half: bool) -> TorchEncoder:
    factory = REGISTRY.get(spec.backend)
    if factory is None:
        known = ", ".join(sorted(REGISTRY))
        raise BackendError(f"모르는 backend입니다: {spec.backend!r} (쓸 수 있는 값: {known})")
    try:
        return factory(spec, device, use_half)
    except BackendError:
        raise
    except Exception as exc:
        raise BackendError(f"모델 {spec.id}를 올리지 못했습니다: {exc}") from exc
