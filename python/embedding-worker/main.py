from contextlib import asynccontextmanager
from dataclasses import dataclass
from io import BytesIO
import asyncio
import os
import warnings

import anyio
from fastapi import FastAPI, File, HTTPException, UploadFile
from PIL import Image, UnidentifiedImageError
from pydantic import BaseModel

from embedder import MODEL_NAME, PRETRAINED, VECTOR_SIZE, Embedder

Image.MAX_IMAGE_PIXELS = 64_000_000
warnings.simplefilter("error", Image.DecompressionBombWarning)


class HealthResponse(BaseModel):
    status: str
    model_loaded: bool
    model: str
    pretrained: str
    device: str
    batch_size: int
    batch_timeout_ms: int
    queued: int


class EmbedResponse(BaseModel):
    vector_size: int
    vector: list[float]
    device: str


embedder = Embedder()


@dataclass
class BatchItem:
    image: Image.Image
    future: asyncio.Future


class EmbeddingBatcher:
    def __init__(self, embedder: Embedder) -> None:
        self.embedder = embedder
        self.max_batch_size = max(1, int(os.getenv("EMBEDDING_BATCH_SIZE", "16")))
        self.batch_timeout_ms = max(0, int(os.getenv("EMBEDDING_BATCH_TIMEOUT_MS", "50")))
        self.queue: asyncio.Queue[BatchItem] = asyncio.Queue()
        self.task: asyncio.Task | None = None

    def start(self) -> None:
        self.task = asyncio.create_task(self._run())

    async def stop(self) -> None:
        if self.task is None:
            return
        self.task.cancel()
        try:
            await self.task
        except asyncio.CancelledError:
            pass

    async def embed(self, image: Image.Image) -> list[float]:
        loop = asyncio.get_running_loop()
        future = loop.create_future()
        await self.queue.put(BatchItem(image=image, future=future))
        return await future

    async def _run(self) -> None:
        while True:
            first = await self.queue.get()
            batch = [first]
            deadline = asyncio.get_running_loop().time() + self.batch_timeout_ms / 1000

            while len(batch) < self.max_batch_size:
                timeout = deadline - asyncio.get_running_loop().time()
                if timeout <= 0:
                    break
                try:
                    batch.append(await asyncio.wait_for(self.queue.get(), timeout=timeout))
                except asyncio.TimeoutError:
                    break

            await self._process(batch)

    async def _process(self, batch: list[BatchItem]) -> None:
        active = [item for item in batch if not item.future.cancelled()]
        if not active:
            return
        try:
            vectors = await anyio.to_thread.run_sync(
                self.embedder.embed_many,
                [item.image for item in active],
            )
        except Exception as error:
            for item in active:
                if not item.future.done():
                    item.future.set_exception(error)
            return

        for item, vector in zip(active, vectors, strict=True):
            if not item.future.done():
                item.future.set_result(vector)


batcher = EmbeddingBatcher(embedder)


@asynccontextmanager
async def lifespan(_: FastAPI):
    await anyio.to_thread.run_sync(embedder.load)
    batcher.start()
    yield
    await batcher.stop()


app = FastAPI(title="sauce-engine embedding worker", version="0.1.0", lifespan=lifespan)


@app.get("/health", response_model=HealthResponse)
def health() -> HealthResponse:
    return HealthResponse(
        status="ok",
        model_loaded=embedder.model is not None,
        model=MODEL_NAME,
        pretrained=PRETRAINED,
        device=embedder.device.type,
        batch_size=batcher.max_batch_size,
        batch_timeout_ms=batcher.batch_timeout_ms,
        queued=batcher.queue.qsize(),
    )


@app.post("/embed", response_model=EmbedResponse)
async def embed(file: UploadFile = File(...)) -> EmbedResponse:
    contents = await file.read()
    if not contents:
        raise HTTPException(status_code=400, detail="empty file")

    try:
        image = Image.open(BytesIO(contents))
        image.load()
        vector = await batcher.embed(image.convert("RGB"))
    except (UnidentifiedImageError, OSError, Image.DecompressionBombWarning) as error:
        raise HTTPException(status_code=400, detail="invalid image") from error
    except RuntimeError as error:
        raise HTTPException(status_code=500, detail=str(error)) from error

    return EmbedResponse(
        vector_size=VECTOR_SIZE,
        vector=vector,
        device=embedder.device.type,
    )
