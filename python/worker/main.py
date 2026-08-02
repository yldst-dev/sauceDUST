"""조립 지점. 어떤 구현이 어떤 자리에 들어가는지 여기서만 정합니다."""

from __future__ import annotations

import logging

import config
from api import create_app
from domain import ModelSpec
from domain.imaging import configure_pillow
from service import EmbedService

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("saucedust.worker")

configure_pillow()
settings = config.load()


def _build_all(specs: list[ModelSpec], device_name: str, use_half: bool) -> list:
    import backends

    encoders = []
    for spec in specs:
        log.info("모델을 올립니다: %s (%s) → %s", spec.id, spec.checkpoint, device_name)
        encoders.append(backends.build(spec, device_name, use_half))
    return encoders


def load_service() -> EmbedService:
    """모델을 올립니다. 무거운 import는 이 안에서만 합니다.

    GPU 적재가 하나라도 실패하면 전부 CPU로 다시 올립니다.
    일부만 CPU에 두면 장치가 섞여 배치가 엉키기 때문입니다.
    """
    import backends
    import device

    chosen = device.select()
    log.info("장치 %s를 씁니다", chosen.name)

    try:
        encoders = _build_all(settings.specs, chosen.name, chosen.use_half)
    except backends.BackendError as exc:
        if chosen.name == device.CPU.name:
            raise
        log.warning("%s 적재에 실패해 전부 CPU로 다시 올립니다 (%s)", chosen.name, exc)
        chosen = device.CPU
        encoders = _build_all(settings.specs, chosen.name, chosen.use_half)

    batch_size = settings.batch_size_override or chosen.batch_size
    return EmbedService(encoders, settings, chosen.name, batch_size)


app = create_app(load_service, settings.batch_window_ms)
