"""장치 정책 시험.

NVIDIA 카드가 없는 곳에서도 정책이 맞는지 확인할 수 있도록,
torch를 부르는 부분과 판단하는 부분을 갈라 두었습니다.
"""

from __future__ import annotations

import pytest

import device


# 소비자용 파스칼(GTX 10 계열)은 fp16 연산이 fp32의 64분의 1 속도입니다.
# CUDA면 무조건 켜던 때가 있었고 그러면 1050 Ti에서 크게 느려졌습니다.
@pytest.mark.parametrize(
    "capability, want",
    [
        ((6, 1), False),   # GTX 1050 Ti, 1080
        ((6, 0), False),   # P100
        ((5, 2), False),   # 맥스웰
        ((7, 0), True),    # V100, 텐서 코어 첫 세대
        ((7, 5), True),    # RTX 20
        ((8, 6), True),    # RTX 30
        ((8, 9), True),    # RTX 40
    ],
)
def test_half_only_where_it_is_faster(capability, want):
    assert device.supports_fast_half(capability) is want


# 4 GB 카드에 배치 64를 잡으면 모델 무게 1.4 GB 위에 활성값이 안 들어갑니다.
@pytest.mark.parametrize(
    "vram_gb, want",
    [(4, 8), (6, 16), (8, 32), (11, 32), (12, 64), (24, 64)],
)
def test_batch_follows_vram(vram_gb, want):
    assert device.batch_for_vram(vram_gb) == want


def test_batch_override_wins():
    d = device.Device(name="cuda", use_half=False, batch_override=8)
    assert d.batch_size == 8

    plain = device.Device(name="cuda", use_half=True)
    assert plain.batch_size == device.BATCH_SIZES["cuda"]


# torch는 판이 올라가며 오래된 세대를 뺍니다. 빠진 판을 깔면 카드가
# 멀쩡해도 커널이 없다는 말만 나오는데, 그 말로는 원인을 알기 어렵습니다.
def test_arch_support_is_checked():
    pascal = (6, 1)
    assert device.arch_supported(pascal, ["sm_61", "sm_75", "sm_86"])
    assert not device.arch_supported(pascal, ["sm_75", "sm_86", "sm_90"])

    # 목록이 비어 있으면 판단하지 않습니다. 막을 근거가 없습니다.
    assert not device.arch_supported(pascal, [])


def test_cpu_constant_unchanged():
    assert device.CPU.name == "cpu"
    assert device.CPU.use_half is False
    assert device.CPU.batch_size == 4
