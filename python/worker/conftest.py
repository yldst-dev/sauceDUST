import sys
from pathlib import Path

# 시험이 워커 모듈을 import할 수 있게 합니다.
sys.path.insert(0, str(Path(__file__).parent))
