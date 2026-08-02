"""장치 선택.

CUDA, MPS, CPU 순으로 고릅니다. 같은 CUDA라도 세대와 VRAM에 따라
fp16 여부와 배치 크기를 다르게 잡습니다.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass

log = logging.getLogger(__name__)

# 장치별 감당할 만한 배치 크기입니다. GPU는 한 장씩 넣으면 대부분 놀게 됩니다.
# CUDA는 VRAM을 보고 따로 정하므로 여기 값은 넉넉한 카드 기준입니다.
BATCH_SIZES = {"cuda": 64, "mps": 16, "cpu": 4}

# fp16이 실제로 빨라지는 첫 세대입니다. Volta부터 텐서 코어가 붙습니다.
#
# 그 아래 소비자용 파스칼(GTX 10 계열)은 fp16을 담을 수는 있어도 연산이
# fp32의 64분의 1 속도입니다. 켜면 빨라지기는커녕 훨씬 느려집니다.
# 여기를 보지 않고 CUDA면 무조건 켜던 때가 있었고, 그러면 1050 Ti 같은
# 카드에서 이유 없이 느렸습니다.
HALF_FROM = (7, 0)

# 모델 무게가 fp32로 둘이 합쳐 1.4 GB쯤 됩니다. 4 GB 카드에서 배치를
# 크게 잡으면 활성값이 들어갈 자리가 남지 않습니다.
VRAM_BATCH = ((12, 64), (8, 32), (6, 16), (0, 8))


@dataclass(frozen=True)
class Device:
    name: str
    use_half: bool
    batch_override: int = 0

    @property
    def batch_size(self) -> int:
        if self.batch_override > 0:
            return self.batch_override
        return BATCH_SIZES.get(self.name, 4)


def supports_fast_half(capability: tuple[int, int]) -> bool:
    """이 세대에서 fp16이 fp32보다 빠른지입니다."""
    return capability >= HALF_FROM


def batch_for_vram(total_gb: float) -> int:
    """VRAM에 맞는 배치 크기입니다."""
    for floor, size in VRAM_BATCH:
        if total_gb >= floor:
            return size
    return 8


def arch_hint(capability: tuple[int, int], arch_list: list[str]) -> str:
    """카드가 안 될 때 왜 그런지 짐작해 적어 줍니다. 막는 데 쓰지 않습니다.

    torch는 판이 올라가면서 오래된 세대를 하나씩 뺍니다. 빠진 판을 깔면
    카드가 멀쩡해도 커널이 없다는 말만 나오는데, 그 말로는 원인을 알기
    어렵습니다.

    이것으로 미리 막지는 않습니다. CUDA는 같은 세대 안에서 cubin이
    앞으로 호환되고 PTX가 있으면 더 새 카드에 맞춰 그 자리에서 컴파일도
    합니다. 목록에 정확히 같은 이름이 없다고 못 쓰는 것이 아닙니다.
    실제로 되는지는 돌려 봐야 압니다.
    """
    want = f"sm_{capability[0]}{capability[1]}"
    if any(a.strip() == want for a in arch_list):
        return ""
    return f"설치된 torch에 {want}가 없습니다. 담긴 것은 {', '.join(arch_list)}입니다"


def cuda_usable(torch) -> tuple[bool, str]:
    """CUDA가 실제로 계산을 해내는지 봅니다.

    목록을 견주는 대신 아주 작은 연산을 시켜 봅니다. 커널이 없거나
    드라이버가 안 맞거나 메모리를 못 잡으면 여기서 드러납니다.
    """
    try:
        torch.zeros(8, device="cuda").add_(1).sum().item()
        return True, ""
    # 무엇이 터지든 CPU로 갑니다. 장치를 고르다 예외를 내면 워커가 아예 못 뜹니다.
    except Exception as exc:
        return False, str(exc)


def select() -> Device:
    import torch

    if torch.cuda.is_available() and (device := _cuda_device(torch)):
        return device
    if torch.backends.mps.is_available() and torch.backends.mps.is_built():
        return Device(name="mps", use_half=False)
    return Device(name="cpu", use_half=False)


def _cuda_device(torch) -> Device | None:
    """쓸 수 있으면 CUDA 장치를, 아니면 None을 냅니다.

    여기서 예외가 새어 나가면 안 됩니다. 부르는 쪽의 CPU 대체는 모델을
    올릴 때만 감싸고 있어서, 장치를 고르다 터지면 워커가 아예 뜨지
    못합니다. 드라이버가 반쯤 올라온 컴퓨터에서는 CUDA가 있다고 해 놓고
    장치 정보를 묻는 것조차 실패할 수 있습니다.
    """
    try:
        ok, why = cuda_usable(torch)
        if not ok:
            log.warning("CUDA를 쓰지 못해 CPU로 갑니다 (%s)%s", why, _hint(torch))
            return None

        capability = torch.cuda.get_device_capability()
        props = torch.cuda.get_device_properties(0)
        return Device(
            name="cuda",
            use_half=supports_fast_half(capability),
            batch_override=batch_for_vram(props.total_memory / 1024**3),
        )
    # 무엇이 터지든 CPU로 갑니다. 느린 것이 못 뜨는 것보다 낫습니다.
    except Exception as exc:
        log.warning("CUDA 장치를 살피다 실패해 CPU로 갑니다 (%s)", exc)
        return None


def _hint(torch) -> str:
    """왜 안 되는지 짐작해 덧붙입니다. 이것 때문에 실패하면 안 됩니다."""
    try:
        hint = arch_hint(torch.cuda.get_device_capability(), torch.cuda.get_arch_list())
    except Exception:
        return ""
    return f". {hint}" if hint else ""



CPU = Device(name="cpu", use_half=False)
