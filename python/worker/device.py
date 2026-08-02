"""장치 선택.

CUDA, MPS, CPU 순으로 고릅니다. 같은 CUDA라도 세대와 VRAM에 따라
fp16 여부와 배치 크기를 다르게 잡습니다.
"""

from __future__ import annotations

from dataclasses import dataclass

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


def arch_supported(capability: tuple[int, int], arch_list: list[str]) -> bool:
    """설치된 torch가 이 카드를 위한 커널을 담고 있는지입니다.

    torch는 판이 올라가면서 오래된 세대를 하나씩 뺍니다. 빠진 판을 깔면
    카드가 멀쩡해도 추론할 때 커널이 없다는 말만 나옵니다. 그 말만 보고
    원인을 찾기 어려우므로 시작할 때 미리 봅니다.
    """
    want = f"sm_{capability[0]}{capability[1]}"
    return any(a.strip() == want for a in arch_list)


class UnsupportedGPU(RuntimeError):
    pass


def select() -> Device:
    import torch

    if torch.cuda.is_available():
        return _cuda_device(torch)
    if torch.backends.mps.is_available() and torch.backends.mps.is_built():
        return Device(name="mps", use_half=False)
    return Device(name="cpu", use_half=False)


def _cuda_device(torch) -> Device:
    capability = torch.cuda.get_device_capability()
    props = torch.cuda.get_device_properties(0)

    arch_list = torch.cuda.get_arch_list()
    if arch_list and not arch_supported(capability, arch_list):
        raise UnsupportedGPU(
            f"{props.name}는 sm_{capability[0]}{capability[1]}인데 "
            f"설치된 torch {torch.__version__}에는 {', '.join(arch_list)}만 있습니다. "
            f"이 카드를 지원하는 판을 깔거나 CUDA 없는 판으로 CPU를 쓰십시오."
        )

    return Device(
        name="cuda",
        use_half=supports_fast_half(capability),
        batch_override=batch_for_vram(props.total_memory / 1024**3),
    )


CPU = Device(name="cpu", use_half=False)
