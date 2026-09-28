"""
Main Application Entry Point

Starts the gRPC server for the audit engine.

This is where everything gets wired together:
1. Load configuration (contracts registry keys; warnings, strict mode)
2. Check the bind address against the internal token (non-loopback needs one)
3. Resolve the audit judge (judge.py) and cap its in-flight calls
4. Initialize the knowledge add-on (embeddings + Qdrant) if enabled; its heavy
   dependencies are imported only then
5. Build the LangGraph audit workflow
6. Start the metrics endpoint and the gRPC server (internal-token interceptor,
   TRUTHTABLE_ENGINE_MAX_RPCS)
"""

import asyncio
import logging
import os
import platform
import signal
import sys
import time
from typing import Any, Optional, Tuple

from truthtable import __version__
from truthtable.config import STRICT_CONFIG_EXIT_CODE, Settings, load_settings
from truthtable.engine_info import (
    KNOWLEDGE_EXTRAS_MISSING,
    KNOWLEDGE_INIT_FAILED,
    EngineContext,
    KnowledgeState,
    knowledge_state_for,
)
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.server import AuditServicer, create_server
from truthtable.internal_auth import (
    InternalTokenError,
    InternalTokenInterceptor,
    check_bind,
    token_source,
)
from truthtable.judge import MOCK_JUDGE_MODEL, OLLAMA, JudgeSelection, resolve_judge
from truthtable.providers.base import CompletionRequest, LLMProvider
from truthtable.providers.limiter import LimitedProvider, configure_process_limiter
from truthtable.providers.pricing import (
    PriceTable,
    PriceTableError,
    load_default_price_table,
    set_active_price_table,
)
from truthtable.contracts import ContractsNotFoundError

__all__ = ["MOCK_JUDGE_MODEL", "init_provider", "main"]


def setup_logging(level: str = "INFO") -> None:
    """Setup application logging."""
    logging.basicConfig(
        level=getattr(logging, level.upper(), logging.INFO),
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[logging.StreamHandler(sys.stdout)],
    )


async def init_provider(
    settings: Settings, logger: logging.Logger
) -> Tuple[Optional[LLMProvider], str]:
    """
    Resolve the audit judge (see truthtable.judge for the order and the
    fallback rules).

    LLM_PROVIDER=mock (the zero-key demo and e2e stacks) answers from the
    contracts scenario pack (AuditOptions.mock_scenario) and never falls back;
    if the pack is missing the engine does not start.

    Returns:
        (provider, effective_provider_name); the provider is None and the name
        "none" when the engine runs without a judge.
    """
    selection = await resolve_judge(settings, log=logger)
    return selection.provider, selection.effective


def configure_langsmith(settings: Settings, logger: logging.Logger) -> bool:
    """
    Enable LangSmith tracing when LANGSMITH_TRACING=true and a key is set (D19).

    Returns:
        Whether tracing was enabled.
    """
    if not settings.langsmith_tracing:
        logger.info("LangSmith tracing off (set LANGSMITH_TRACING=true to enable)")
        return False
    api_key = settings.langsmith_api_key.get_secret_value()
    if not api_key:
        logger.warning("LANGSMITH_TRACING=true but LANGSMITH_API_KEY is not set: tracing stays off")
        return False
    for prefix in ("LANGCHAIN", "LANGSMITH"):
        os.environ[f"{prefix}_API_KEY"] = api_key
        os.environ[f"{prefix}_PROJECT"] = settings.langsmith_project
    os.environ["LANGCHAIN_TRACING_V2"] = "true"
    os.environ["LANGSMITH_TRACING"] = "true"
    logger.info(f"LangSmith tracing enabled (project: {settings.langsmith_project})")
    return True


def load_price_table(settings: Settings, logger: logging.Logger) -> None:
    """Install TRUTHTABLE_PRICE_TABLE_FILE (else the contracts table) for cost reporting."""
    try:
        if settings.price_table_file:
            table = PriceTable.load(settings.price_table_file)
        else:
            table = load_default_price_table()
    except (OSError, PriceTableError, ContractsNotFoundError) as exc:
        logger.warning(f"Price table unavailable ({exc}); LLM costs are reported unknown")
        return
    set_active_price_table(table)
    logger.info(f"Price table {table.version} ({len(table.entries)} entries)")


async def warm_up(provider: LLMProvider, logger: logging.Logger) -> None:
    """
    Ollama loads the model into memory on the first completion, which can take
    long enough to time out the first real audit. Pay that cost at startup.
    """
    logger.info("Warming up the judge model (first load can take a while)...")
    try:
        start = time.time()
        await provider.complete(
            CompletionRequest(
                messages=provider.create_messages(
                    system_prompt="Reply with the single word: OK",
                    user_message="OK?",
                ),
                model=provider.model,
                temperature=0.0,
                max_tokens=4,
            )
        )
        logger.info(f"Judge model warmed up in {time.time() - start:.1f}s")
    except Exception as exc:  # warm-up is best-effort
        logger.warning(f"Model warm-up failed (continuing anyway): {exc}")


def init_knowledge(
    settings: Settings, provider: Optional[LLMProvider], logger: logging.Logger
) -> Tuple[Any, Any, Any, Any, KnowledgeState]:
    """
    Start the knowledge add-on when TRUTHTABLE_KNOWLEDGE_ENABLED and QDRANT_URL
    are set. Its dependencies (sentence-transformers, torch, qdrant-client) are
    the `knowledge` extra and are imported only here.

    Returns:
        (embedding_service, qdrant_store, hybrid_retriever, ingestor, state);
        the first four are None when knowledge is off.
    """
    state = knowledge_state_for(settings)
    if not state.enabled:
        if settings.qdrant_url and not settings.knowledge_enabled:
            logger.info(
                "Knowledge add-on off (TRUTHTABLE_KNOWLEDGE_ENABLED=false); QDRANT_URL unused"
            )
        else:
            logger.info(f"Knowledge add-on off ({state.reason})")
        return None, None, None, None, state

    logger.info("Initializing the knowledge add-on...")
    try:
        from truthtable.vectorstore.embeddings import EmbeddingService
        from truthtable.vectorstore.qdrant_store import QdrantStore
    except ImportError as exc:
        logger.error(
            f"Knowledge add-on unavailable: {exc}. Install the `knowledge` extra "
            "(poetry install --extras knowledge; ENGINE_EXTRAS=knowledge for the image)"
        )
        return None, None, None, None, KnowledgeState(reason=f"{KNOWLEDGE_EXTRAS_MISSING}: {exc}")

    try:
        logger.info(f"Loading embedding model {settings.embedding_model}...")
        embedding_service = EmbeddingService(model_name=settings.embedding_model)
        logger.info(f"Embedding model loaded (dimension={embedding_service.dimension})")

        logger.info(f"Connecting to Qdrant at {settings.qdrant_url}...")
        qdrant_store = QdrantStore(
            url=settings.qdrant_url, vector_dimension=embedding_service.dimension
        )
        qdrant_store.ensure_collection()
        doc_count = qdrant_store.count()
        logger.info(f"Qdrant connected. Knowledge base has {doc_count} points.")
        if doc_count == 0:
            logger.warning("Knowledge base is empty! Run: python scripts/seed_knowledge.py")
    except Exception as exc:
        logger.error(f"Failed to initialize the knowledge add-on: {exc}")
        logger.warning("Continuing without knowledge retrieval")
        return None, None, None, None, KnowledgeState(reason=f"{KNOWLEDGE_INIT_FAILED}: {exc}")

    # Knowledge-base components (VERITAS-lite): claim-level ingestion with the
    # Gate-1 entailment check, contradiction detection, and hybrid (BM25 +
    # dense RRF) retrieval. Ingestion needs the judge.
    from truthtable.kb.hybrid import HybridClaimRetriever

    hybrid_retriever = HybridClaimRetriever(
        embedding_service=embedding_service, qdrant_store=qdrant_store
    )
    ingestor = None
    if provider is not None:
        from truthtable.kb import ClaimIngestor, ContradictionDetector

        ingestor = ClaimIngestor(
            provider=provider,
            embedding_service=embedding_service,
            qdrant_store=qdrant_store,
            detector=ContradictionDetector(provider=provider),
            on_change=hybrid_retriever.mark_dirty,
        )
        logger.info("Knowledge base enabled: Gate-1 ingestion + hybrid retrieval")
    return embedding_service, qdrant_store, hybrid_retriever, ingestor, state


def start_metrics(settings: Settings, logger: logging.Logger) -> None:
    """Serve Prometheus metrics on METRICS_PORT (D15)."""
    try:
        from prometheus_client import start_http_server

        start_http_server(settings.metrics_port)
        logger.info(f"Metrics server listening on :{settings.metrics_port}")
    except Exception as exc:
        logger.warning(f"Failed to start metrics server on :{settings.metrics_port}: {exc}")


async def main() -> None:
    """Main application entry point."""
    loaded = load_settings()
    settings = loaded.settings
    setup_logging(settings.effective_log_level)

    logger = logging.getLogger(__name__)
    logger.info(f"Starting TrustAgent Audit Engine {__version__}")
    for warning in loaded.warnings:
        logger.warning(f"Configuration: {warning}")
    if loaded.strict_failure:
        logger.error(
            f"TRUTHTABLE_STRICT_CONFIG=true and {len(loaded.warnings)} configuration "
            "warning(s): refusing to start"
        )
        raise SystemExit(STRICT_CONFIG_EXIT_CODE)

    # contracts.py reads TRUTHTABLE_CONTRACTS_DIR from the process environment;
    # hand it a value that came from a dotenv file (the environment still wins).
    if settings.contracts_dir.strip().lower() not in ("", "auto"):
        os.environ.setdefault("TRUTHTABLE_CONTRACTS_DIR", settings.contracts_dir)

    configure_langsmith(settings, logger)
    load_price_table(settings, logger)

    # A non-loopback bind needs the internal token (waits for the proxy's file).
    tokens = token_source(settings.internal_token.get_secret_value(), settings.internal_token_file)
    try:
        await check_bind(settings.grpc_host, tokens)
    except InternalTokenError as exc:
        logger.error(str(exc))
        raise SystemExit(STRICT_CONFIG_EXIT_CODE) from None

    judge: JudgeSelection = await resolve_judge(settings, log=logger)
    limiter = configure_process_limiter(settings.judge_max_inflight)
    provider: Optional[LLMProvider] = (
        LimitedProvider(judge.provider, limiter) if judge.provider is not None else None
    )
    if provider is not None and judge.effective == OLLAMA:
        await warm_up(provider, logger)

    embedding_service, qdrant_store, hybrid_retriever, ingestor, knowledge = init_knowledge(
        settings, provider, logger
    )

    audit_graph = None
    if provider is not None:
        logger.info("Building audit graph...")
        audit_graph = build_audit_graph(
            provider=provider,
            embedding_service=embedding_service,
            qdrant_store=qdrant_store,
            hybrid_retriever=hybrid_retriever,
            verifier_concurrency=settings.verifier_concurrency,
        )
        logger.info("Audit graph ready")
    else:
        logger.warning("No audit judge: every deep audit is NOT_ASSESSED (no_judge)")

    start_metrics(settings, logger)

    engine = EngineContext(
        settings=settings,
        judge=judge,
        knowledge=knowledge,
        warnings=[str(warning) for warning in loaded.warnings],
    )
    servicer = AuditServicer(
        audit_graph=audit_graph,
        provider=provider,
        qdrant_store=qdrant_store,
        embedding_service=embedding_service,
        ingestor=ingestor,
        engine=engine,
    )
    server = create_server(
        servicer,
        host=settings.grpc_host,
        port=settings.grpc_port,
        max_rpcs=settings.engine_max_rpcs,
        interceptors=[InternalTokenInterceptor(tokens)],
    )

    await server.start()
    logger.info(
        f"gRPC server listening on {settings.grpc_host}:{settings.grpc_port} "
        f"(max {settings.engine_max_rpcs} RPCs, {settings.engine_max_audits} audits; "
        f"internal token {'required' if tokens.current() else 'not configured'})"
    )
    logger.info("TrustAgent Audit Engine is ready!")

    # Setup graceful shutdown (cross-platform)
    # WHY: loop.add_signal_handler() only works on Unix, not Windows.
    # On Windows, we rely on the KeyboardInterrupt caught in __main__.
    async def shutdown(sig_name: str) -> None:
        logger.info(f"Received {sig_name}, shutting down...")
        await server.stop(grace=5)
        logger.info("Server stopped gracefully")

    if platform.system() != "Windows":
        loop = asyncio.get_running_loop()
        tasks: "set[asyncio.Task[None]]" = set()

        def on_signal(sig: signal.Signals) -> None:
            task = asyncio.create_task(shutdown(sig.name))
            tasks.add(task)
            task.add_done_callback(tasks.discard)

        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, on_signal, sig)
    else:
        logger.info("Running on Windows - using KeyboardInterrupt for shutdown")

    try:
        await server.wait_for_termination()
    finally:
        if provider is not None:
            await provider.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nShutdown requested by user")
    except SystemExit:
        raise
    except Exception as e:
        logging.error(f"Fatal error: {e}", exc_info=True)
        sys.exit(1)
