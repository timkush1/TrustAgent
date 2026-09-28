"""
Main Application Entry Point

Starts the gRPC server for the audit engine.

This is where everything gets wired together:
1. Load configuration from environment variables
2. Initialize the LLM provider (Ollama)
3. Initialize RAG components (embeddings + Qdrant) if configured
4. Build the LangGraph audit workflow
5. Start the gRPC server
"""

import asyncio
import logging
import os
import platform
import signal
import sys

from truthtable.config import get_settings
from truthtable.contracts import DEMO_SCENARIOS, contract_path
from truthtable.providers.registry import get_provider
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.server import AuditServicer, create_server

# Reported model of the mock judge unless LLM_MODEL names one.
MOCK_JUDGE_MODEL = "mock"


def setup_logging(level: str = "INFO"):
    """Setup application logging."""
    logging.basicConfig(
        level=getattr(logging, level.upper()),
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[logging.StreamHandler(sys.stdout)],
    )


async def init_provider(settings, logger):
    """Build the configured LLM provider, falling back to local Ollama when a
    cloud provider is unavailable (missing API key or failed health check), so
    the engine always comes up.

    LLM_PROVIDER=mock (the zero-key demo and e2e stacks) is the exception: the
    mock judge answers from the contracts scenario pack (AuditOptions.mock_scenario)
    and never falls back; if the pack is missing the engine does not start.

    Returns:
        (provider, effective_provider_name)
    """
    name = (settings.llm_provider or "ollama").lower()

    def _build(provider_name: str, model: str):
        kwargs = {"model": model}
        if provider_name == "ollama":
            kwargs["base_url"] = settings.ollama_base_url
        elif provider_name == "anthropic" and settings.anthropic_api_key:
            kwargs["api_key"] = settings.anthropic_api_key
        elif provider_name == "openai" and settings.openai_api_key:
            kwargs["api_key"] = settings.openai_api_key
        elif provider_name == "mock":
            kwargs["scenarios_path"] = contract_path(DEMO_SCENARIOS)
        return get_provider(provider_name, **kwargs)

    if name == "mock":
        explicit_model = "llm_model" in getattr(settings, "model_fields_set", set())
        provider = _build("mock", settings.llm_model if explicit_model else MOCK_JUDGE_MODEL)
        logger.info(
            f"Using the mock judge (scenario fixtures from {DEMO_SCENARIOS}); "
            "audit verdicts are simulated"
        )
        return provider, "mock"

    try:
        logger.info(f"Initializing {name} provider (model: {settings.llm_model})...")
        provider = _build(name, settings.llm_model)
        if await provider.health_check():
            logger.info(f"Provider '{name}' is healthy")
            return provider, name
        logger.warning(f"Provider '{name}' health check failed")
    except Exception as exc:  # missing key, bad config, network, etc.
        logger.warning(f"Provider '{name}' could not be initialized: {exc}")

    if name == "ollama":
        # Already local; nothing better to fall back to — continue regardless.
        logger.warning("Continuing with Ollama despite a failed health check")
        return _build("ollama", settings.llm_model), "ollama"

    logger.warning(
        f"Falling back to local Ollama (model: {settings.ollama_fallback_model}). "
        f"Set a valid {name.upper()}_API_KEY to use {name} for higher-quality audits."
    )
    fallback = _build("ollama", settings.ollama_fallback_model)
    if await fallback.health_check():
        logger.info("Ollama fallback is healthy")
    else:
        logger.warning("Ollama fallback health check failed - continuing anyway")
    return fallback, "ollama"


async def main():
    """Main application entry point."""
    # Load settings
    settings = get_settings()
    setup_logging(settings.log_level)

    logger = logging.getLogger(__name__)
    logger.info("Starting TrustAgent Audit Engine")
    logger.info(f"Configured provider: {settings.llm_provider}, model: {settings.llm_model}")

    # Setup LangSmith tracing (optional)
    if settings.langsmith_api_key:
        os.environ["LANGCHAIN_TRACING_V2"] = "true"
        os.environ["LANGCHAIN_API_KEY"] = settings.langsmith_api_key
        os.environ["LANGCHAIN_PROJECT"] = settings.langsmith_project
        logger.info(f"LangSmith tracing enabled (project: {settings.langsmith_project})")
    else:
        logger.info("LangSmith tracing disabled (set LANGSMITH_API_KEY to enable)")

    # Initialize the LLM provider with automatic fallback to local Ollama when a
    # cloud provider is unavailable (missing key / failed health check).
    provider, effective_provider = await init_provider(settings, logger)
    judge_fallback_active = effective_provider != (settings.llm_provider or "ollama").lower()

    # Warm-up: Ollama loads the model into memory on the first completion,
    # which can take long enough to time out the first real audit. Pay that
    # cost here instead, before we accept traffic.
    if effective_provider == "ollama":
        import time

        from truthtable.providers.base import CompletionRequest

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

    # Initialize RAG components (if Qdrant URL is configured)
    embedding_service = None
    qdrant_store = None

    if settings.qdrant_url and settings.rag_enabled:
        logger.info("Initializing RAG components...")
        try:
            from truthtable.vectorstore.embeddings import EmbeddingService
            from truthtable.vectorstore.qdrant_store import QdrantStore

            # Load embedding model (takes ~2s on first load)
            logger.info("Loading embedding model...")
            embedding_service = EmbeddingService()
            logger.info(f"Embedding model loaded (dimension={embedding_service.dimension})")

            # Connect to Qdrant
            logger.info(f"Connecting to Qdrant at {settings.qdrant_url}...")
            qdrant_store = QdrantStore(
                url=settings.qdrant_url,
                vector_dimension=embedding_service.dimension,
            )
            qdrant_store.ensure_collection()

            doc_count = qdrant_store.count()
            logger.info(f"Qdrant connected. Knowledge base has {doc_count} documents.")

            if doc_count == 0:
                logger.warning("Knowledge base is empty! " "Run: python scripts/seed_knowledge.py")

        except Exception as e:
            logger.error(f"Failed to initialize RAG components: {e}")
            logger.warning("Continuing without knowledge retrieval")
            embedding_service = None
            qdrant_store = None
    elif settings.qdrant_url and not settings.rag_enabled:
        logger.info("RAG disabled (rag_enabled=false). Running without knowledge retrieval.")
    else:
        logger.warning(
            "QDRANT_URL not set. Running without knowledge retrieval. "
            "Set QDRANT_URL=http://localhost:6333 to enable."
        )

    # Knowledge-base components (VERITAS-lite): claim-level ingestion with
    # the Gate-1 entailment check, contradiction detection, and hybrid
    # (BM25 + dense RRF) retrieval. Requires both the LLM and the vector store.
    ingestor = None
    hybrid_retriever = None
    if embedding_service is not None and qdrant_store is not None:
        from truthtable.kb import ClaimIngestor, ContradictionDetector
        from truthtable.kb.hybrid import HybridClaimRetriever

        hybrid_retriever = HybridClaimRetriever(
            embedding_service=embedding_service, qdrant_store=qdrant_store
        )
        ingestor = ClaimIngestor(
            provider=provider,
            embedding_service=embedding_service,
            qdrant_store=qdrant_store,
            detector=ContradictionDetector(provider=provider),
            on_change=hybrid_retriever.mark_dirty,
        )
        logger.info("Knowledge base enabled: Gate-1 ingestion + hybrid retrieval")

    # Build audit graph (with or without retrieval)
    logger.info("Building audit graph...")
    audit_graph = build_audit_graph(
        provider=provider,
        embedding_service=embedding_service,
        qdrant_store=qdrant_store,
        hybrid_retriever=hybrid_retriever,
        verifier_concurrency=settings.verifier_concurrency,
    )
    logger.info("Audit graph ready")

    # Start Prometheus metrics HTTP server
    metrics_port = 8001
    try:
        from prometheus_client import start_http_server

        start_http_server(metrics_port)
        logger.info(f"Metrics server listening on :{metrics_port}")
    except Exception as e:
        logger.warning(f"Failed to start metrics server: {e}")

    # Create gRPC server
    logger.info(f"Creating gRPC server on {settings.grpc_host}:{settings.grpc_port}...")
    servicer = AuditServicer(
        audit_graph=audit_graph,
        provider=provider,
        qdrant_store=qdrant_store,
        embedding_service=embedding_service,
        ingestor=ingestor,
        judge_provider=effective_provider,
        judge_fallback_active=judge_fallback_active,
    )
    server = create_server(servicer, host=settings.grpc_host, port=settings.grpc_port)

    # Start server
    await server.start()
    logger.info(f"gRPC server listening on {settings.grpc_host}:{settings.grpc_port}")
    logger.info("TrustAgent Audit Engine is ready!")

    # Setup graceful shutdown (cross-platform)
    # WHY: loop.add_signal_handler() only works on Unix, not Windows.
    # On Windows, we rely on the KeyboardInterrupt caught in __main__.
    async def shutdown(sig_name):
        logger.info(f"Received {sig_name}, shutting down...")
        await server.stop(grace=5)
        logger.info("Server stopped gracefully")

    if platform.system() != "Windows":
        loop = asyncio.get_event_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, lambda s=sig: asyncio.create_task(shutdown(s.name)))
    else:
        logger.info("Running on Windows - using KeyboardInterrupt for shutdown")

    # Wait for termination
    await server.wait_for_termination()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nShutdown requested by user")
    except Exception as e:
        logging.error(f"Fatal error: {e}", exc_info=True)
        sys.exit(1)
