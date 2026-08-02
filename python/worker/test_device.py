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


# 목록에 정확히 같은 이름이 없다고 못 쓰는 것이 아닙니다. CUDA는 같은
# 세대 안에서 cubin이 앞으로 호환되고 PTX가 있으면 그 자리에서 컴파일도
# 합니다. 그래서 이것은 안 될 때 이유를 적어 주는 데만 씁니다.
def test_arch_hint_only_explains():
    assert device.arch_hint((6, 1), ["sm_61", "sm_75"]) == ""

    hint = device.arch_hint((6, 1), ["sm_75", "sm_86"])
    assert "sm_61" in hint and "sm_75" in hint


class _FailingCuda:
    """CUDA가 있다고는 하는데 실제로는 못 쓰는 상황입니다."""

    class cuda:
        @staticmethod
        def is_available():
            return True

        @staticmethod
        def get_device_capability():
            return (6, 1)

        @staticmethod
        def get_arch_list():
            return ["sm_75", "sm_86"]

    class backends:
        class mps:
            @staticmethod
            def is_available():
                return False

            @staticmethod
            def is_built():
                return False

    @staticmethod
    def zeros(*_args, **_kwargs):
        raise RuntimeError("no kernel image is available for execution on the device")


# 장치를 고르다 예외를 던지면 워커가 아예 뜨지 못합니다. 부르는 쪽의
# CPU 대체는 모델을 올릴 때만 감싸고 있기 때문입니다.
def test_unusable_cuda_falls_back_instead_of_raising(monkeypatch):
    ok, why = device.cuda_usable(_FailingCuda)
    assert ok is False
    assert "kernel image" in why

    assert device._cuda_device(_FailingCuda) is None


def test_cpu_constant_unchanged():
    assert device.CPU.name == "cpu"
    assert device.CPU.use_half is False
    assert device.CPU.batch_size == 4
