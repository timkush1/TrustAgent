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
from truthtable.providers.registry import get_provider
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.server import AuditServicer, create_server


def setup_logging(level: str = "INFO"):
    """Setup application logging."""
    logging.basicConfig(
        level=getattr(logging, level.upper()),
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[logging.StreamHandler(sys.stdout)],
    )


async def main():
    """Main application entry point."""
    # Load settings
    settings = get_settings()
    setup_logging(settings.log_level)

    logger = logging.getLogger(__name__)
    logger.info("Starting TruthTable Audit Engine")
    logger.info(f"Provider: {settings.llm_provider}, Model: {settings.llm_model}")

    # Setup LangSmith tracing (optional)
    if settings.langsmith_api_key:
        os.environ["LANGCHAIN_TRACING_V2"] = "true"
        os.environ["LANGCHAIN_API_KEY"] = settings.langsmith_api_key
        os.environ["LANGCHAIN_PROJECT"] = settings.langsmith_project
        logger.info(f"LangSmith tracing enabled (project: {settings.langsmith_project})")
    else:
        logger.info("LangSmith tracing disabled (set LANGSMITH_API_KEY to enable)")

    # Initialize LLM provider. Only Ollama takes a base_url from settings;
    # cloud providers use their own defaults and read API keys from env
    # (OPENAI_API_KEY / ANTHROPIC_API_KEY).
    logger.info(f"Initializing {settings.llm_provider} provider...")
    provider_kwargs = {"model": settings.llm_model}
    if settings.llm_provider == "ollama":
        provider_kwargs["base_url"] = settings.ollama_base_url
    provider = get_provider(settings.llm_provider, **provider_kwargs)

    # Health check
    logger.info("Performing provider health check...")
    if await provider.health_check():
        logger.info("Provider is healthy")
    else:
        logger.warning("Provider health check failed - continuing anyway")

    # Initialize RAG components (if Qdrant URL is configured)
    embedding_service = None
    qdrant_store = None

    if settings.qdrant_url:
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
    )
    server = create_server(servicer, host=settings.grpc_host, port=settings.grpc_port)

    # Start server
    await server.start()
    logger.info(f"gRPC server listening on {settings.grpc_host}:{settings.grpc_port}")
    logger.info("TruthTable Audit Engine is ready!")

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
