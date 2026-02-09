"""
Main Application Entry Point

Starts the gRPC server for the audit engine.
"""

import asyncio
import logging
import signal
import sys
from grpc import aio

from truthtable.config import get_settings
from truthtable.providers.registry import get_provider
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.server import AuditServicer, create_server


# Configure logging
def setup_logging(level: str = "INFO"):
    """Setup application logging."""
    logging.basicConfig(
        level=getattr(logging, level.upper()),
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=[logging.StreamHandler(sys.stdout)]
    )


async def main():
    """Main application entry point."""
    # Load settings
    settings = get_settings()
    setup_logging(settings.log_level)
    
    logger = logging.getLogger(__name__)
    logger.info("Starting TruthTable Audit Engine")
    logger.info(f"Provider: {settings.llm_provider}, Model: {settings.llm_model}")
    
    # Initialize LLM provider
    logger.info(f"Initializing {settings.llm_provider} provider...")
    provider = get_provider(
        settings.llm_provider,
        model=settings.llm_model,
        base_url=settings.ollama_base_url
    )
    
    # Health check
    logger.info("Performing provider health check...")
    if await provider.health_check():
        logger.info("✓ Provider is healthy")
    else:
        logger.warning("⚠ Provider health check failed - continuing anyway")
    
    # Build audit graph
    logger.info("Building audit graph...")
    audit_graph = build_audit_graph(provider)
    logger.info("✓ Audit graph ready")
    
    # Create gRPC server
    logger.info(f"Creating gRPC server on {settings.grpc_host}:{settings.grpc_port}...")
    servicer = AuditServicer(audit_graph=audit_graph, provider=provider)
    server = create_server(servicer, host=settings.grpc_host, port=settings.grpc_port)
    
    # Start server
    await server.start()
    logger.info(f"✓ gRPC server listening on {settings.grpc_host}:{settings.grpc_port}")
    logger.info("TruthTable Audit Engine is ready!")
    
    # Setup graceful shutdown
    async def shutdown(sig):
        logger.info(f"Received signal {sig}, shutting down...")
        await server.stop(grace=5)
        logger.info("Server stopped gracefully")
    
    # Register signal handlers
    loop = asyncio.get_event_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, lambda s=sig: asyncio.create_task(shutdown(s)))
    
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
