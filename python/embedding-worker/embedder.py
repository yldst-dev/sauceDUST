from threading import Lock

import open_clip
import torch
import torch.nn.functional as F
from PIL import Image

MODEL_NAME = "ViT-B-32"
PRETRAINED = "laion2b_s34b_b79k"
VECTOR_SIZE = 512


class Embedder:
    def __init__(self) -> None:
        self.device = self._select_device()
        self.model: torch.nn.Module | None = None
        self.preprocess = None
        self.lock = Lock()

    def load(self) -> None:
        try:
            self._load_on_device(self.device)
        except Exception:
            if self.device.type == "mps":
                self.device = torch.device("cpu")
                self._load_on_device(self.device)
            else:
                raise

    def embed(self, image: Image.Image) -> list[float]:
        return self.embed_many([image])[0]

    def embed_many(self, images: list[Image.Image]) -> list[list[float]]:
        if self.model is None or self.preprocess is None:
            raise RuntimeError("model is not loaded")
        if not images:
            return []

        tensor = torch.stack([self.preprocess(image.convert("RGB")) for image in images]).to(
            self.device
        )
        with self.lock:
            with torch.inference_mode():
                features = self.model.encode_image(tensor)
                features = F.normalize(features, dim=-1)

        vectors = features.detach().cpu().float().tolist()
        for vector in vectors:
            if len(vector) != VECTOR_SIZE:
                raise RuntimeError(f"unexpected vector size: {len(vector)}")
        return vectors

    def _load_on_device(self, device: torch.device) -> None:
        model, _, preprocess = open_clip.create_model_and_transforms(
            MODEL_NAME,
            pretrained=PRETRAINED,
            device=device,
        )
        model.eval()
        self.model = model
        self.preprocess = preprocess

    @staticmethod
    def _select_device() -> torch.device:
        if hasattr(torch.backends, "mps") and torch.backends.mps.is_available():
            return torch.device("mps")
        return torch.device("cpu")
