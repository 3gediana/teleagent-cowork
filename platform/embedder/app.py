"""
A3C Embedding Microservice
==========================

Exposes bge-base-zh-v1.5 as an HTTP /embed endpoint so the Go backend can
compute sentence embeddings without linking a PyTorch runtime.

Endpoints
---------
GET  /health              → liveness + model info
POST /embed               → batch encode texts → vectors (768-dim, normalized)

Why separate service?
---------------------
The Go process cannot easily host a PyTorch / SentenceTransformers model.
A tiny FastAPI sidecar is the path of least resistance: one-time model load,
batched GPU encode, simple REST. Keeps the Go side model-agnostic — swapping
to a different embedder later is one env var change.

Model distribution
------------------
The model is downloaded from HuggingFace on first launch to the project-local
`models/` directory (gitignored, ~400MB). Subsequent launches are offline —
this cache is the source of truth once populated. No absolute paths leak
into the codebase, so any developer can clone and run.

Config (env vars, all optional)
-------------------------------
BGE_MODEL        : HF model ID or local path. Default "BAAI/bge-base-zh-v1.5".
BGE_CACHE_DIR    : where HF saves / looks up model files. Default
                   `platform/embedder/models/` (project-local).
BGE_DEVICE       : "cuda" | "cpu" | "auto" (default "auto").
BGE_BATCH        : encode batch size (default 32).
EMBEDDER_PORT    : listen port (default 3011).
HF_ENDPOINT      : custom HuggingFace mirror (e.g. for mainland China users:
                   "https://hf-mirror.com"). Honoured by huggingface_hub.
"""

from __future__ import annotations

import logging
import os
from pathlib import Path
from typing import List

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
log = logging.getLogger("embedder")

# -- Config ---------------------------------------------------------------

# HuggingFace model ID by default — sentence-transformers downloads on demand
# into BGE_CACHE_DIR and reuses the local copy on subsequent runs. Setting
# BGE_MODEL to an absolute path (e.g. a pre-downloaded snapshot folder) is
# also supported and skips the download entirely.
MODEL_ID: str = os.getenv("BGE_MODEL", "BAAI/bge-base-zh-v1.5")

# Project-local cache dir keeps the model beside the code, so deployment is
# "copy the folder" if you want to ship it offline. Override with
# BGE_CACHE_DIR=/some/other/dir if you want to share one cache across apps.
DEFAULT_CACHE_DIR = str(Path(__file__).resolve().parent / "models")
CACHE_DIR: str = os.getenv("BGE_CACHE_DIR", DEFAULT_CACHE_DIR)

DEVICE_PREF: str = os.getenv("BGE_DEVICE", "auto").lower()
BATCH_SIZE: int = int(os.getenv("BGE_BATCH", "32"))

# bge-zh's recommended query prefix (retrieval-augmented setting). When
# `is_query=True` the caller signals that these texts are search queries
# that will be matched against previously-encoded "documents" (artifacts,
# episode summaries, etc.). The prefix asymmetry is what the bge-zh model
# was fine-tuned for; not using it costs 5-15% retrieval quality.
BGE_QUERY_PREFIX = "为这个句子生成表示以用于检索相关文章："

# -- Model load (eager, at import time) -----------------------------------

def _resolve_device() -> str:
    if DEVICE_PREF in ("cuda", "cpu"):
        return DEVICE_PREF
    try:
        import torch  # type: ignore

        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


DEVICE = _resolve_device()

# Ensure cache dir exists before the first download.
Path(CACHE_DIR).mkdir(parents=True, exist_ok=True)

# If the model is already present locally (blobs + snapshots populated by
# a previous run, a manual copy, or huggingface-cli download), force
# offline mode. This is what stops sentence-transformers from hanging on
# an HTTPS HEAD check against huggingface.co when the network is slow or
# blocked (the classic GFW scenario on Windows). Can be overridden by
# setting BGE_FORCE_ONLINE=1 if you explicitly want to check for updates.
_local_snapshot_root = Path(CACHE_DIR) / f"models--{MODEL_ID.replace('/', '--')}" / "snapshots"
if _local_snapshot_root.exists() and any(_local_snapshot_root.iterdir()) \
        and not os.getenv("BGE_FORCE_ONLINE"):
    os.environ.setdefault("HF_HUB_OFFLINE", "1")
    os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")
    log.info("Found local model cache at %s — running in offline mode "
             "(set BGE_FORCE_ONLINE=1 to re-check for updates).",
             _local_snapshot_root)

log.info("Loading model '%s' on %s (cache_dir=%s) ...", MODEL_ID, DEVICE, CACHE_DIR)

# Import here so the dependency is only required if the service is actually
# started (lets `python -c "import app"` fail loudly but with a clear error).
from sentence_transformers import SentenceTransformer  # noqa: E402

try:
    _MODEL = SentenceTransformer(
        MODEL_ID,
        device=DEVICE,
        cache_folder=CACHE_DIR,
    )
except Exception as exc:  # pragma: no cover — startup failure is terminal
    raise RuntimeError(
        f"Failed to load SentenceTransformer model '{MODEL_ID}' "
        f"(cache_folder={CACHE_DIR}). If this is a first-time run, check "
        f"network access to huggingface.co (or set HF_ENDPOINT to a mirror). "
        f"If the model is pre-downloaded, point BGE_MODEL to the snapshot "
        f"directory. Underlying error: {exc}"
    ) from exc

_DIM: int = _MODEL.get_sentence_embedding_dimension()
log.info("Model ready. Dim=%d", _DIM)

# -- HTTP API -------------------------------------------------------------

app = FastAPI(title="A3C Embedder", version="1.0.0")


class EmbedRequest(BaseModel):
    texts: List[str] = Field(..., description="Texts to encode. May be empty.")
    is_query: bool = Field(
        False,
        description=(
            "If true, applies bge-zh's query prefix. Set True for search queries "
            "(e.g. task description looking up similar artifacts); False for "
            "documents being indexed (artifacts, episodes)."
        ),
    )


class EmbedResponse(BaseModel):
    vectors: List[List[float]]
    dim: int
    device: str
    count: int


@app.get("/health")
def health() -> dict:
    return {
        "status": "ok",
        "model": MODEL_ID,
        "cache_dir": CACHE_DIR,
        "dim": _DIM,
        "device": DEVICE,
        "batch_size": BATCH_SIZE,
    }


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    if not req.texts:
        return EmbedResponse(vectors=[], dim=_DIM, device=DEVICE, count=0)

    # Cap single-request batch to something sane; Go client is responsible
    # for chunking larger jobs, but we add a server-side cap as defense in
    # depth against accidental multi-MB bodies.
    if len(req.texts) > 256:
        raise HTTPException(
            status_code=413,
            detail=f"Too many texts in one request ({len(req.texts)}); split into batches ≤ 256.",
        )

    texts = req.texts
    if req.is_query:
        texts = [BGE_QUERY_PREFIX + t for t in texts]

    # normalize_embeddings=True gives unit-length vectors so cosine
    # similarity reduces to a plain dot product on the Go side.
    vecs = _MODEL.encode(
        texts,
        batch_size=BATCH_SIZE,
        normalize_embeddings=True,
        convert_to_numpy=True,
        show_progress_bar=False,
    )

    return EmbedResponse(
        vectors=vecs.tolist(),
        dim=_DIM,
        device=DEVICE,
        count=len(texts),
    )


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("EMBEDDER_PORT", "3011"))
    uvicorn.run(app, host="127.0.0.1", port=port, log_level="info")
