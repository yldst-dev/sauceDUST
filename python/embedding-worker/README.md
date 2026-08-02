# Embedding Worker

FastAPI 기반 OpenCLIP 이미지 임베딩 워커입니다. `ViT-B-32`와 `laion2b_s34b_b79k`를 사용하며, Apple Silicon MPS 사용 가능 시 `mps`, 불가능하면 `cpu`로 실행합니다.

## 설치

```bash
cd python/embedding-worker
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

## 실행

```bash
uvicorn main:app --host 0.0.0.0 --port 8100
```

## 상태 확인

```bash
curl http://localhost:8100/health
```

## 임베딩 생성

```bash
curl -X POST http://localhost:8100/embed \
  -F "file=@tests/fixtures/query.jpg"
```

응답은 `vector_size`, `vector`, `device` 필드를 포함합니다.

## Micro-batch

`/embed` 요청은 내부 큐에서 짧게 모인 뒤 batch tensor로 추론됩니다.

```bash
EMBEDDING_BATCH_SIZE=16
EMBEDDING_BATCH_TIMEOUT_MS=50
```
