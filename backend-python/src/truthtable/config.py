"""
Configuration Management

Uses Pydantic Settings for type-safe configuration with environment variables.
"""

from pydantic_settings import BaseSettings
from pydantic import Field
from typing import Optional


class Settings(BaseSettings):
    """
    Application settings loaded from environment variables.

    All settings can be overridden via environment variables.
    Example: export OLLAMA_BASE_URL="http://localhost:11434"
    """

    # LLM Provider Settings. Default is Anthropic Claude Haiku for high-quality
    # NLI verification; the engine automatically falls back to local Ollama when
    # no ANTHROPIC_API_KEY is set or the provider is unreachable (see main.py).
    llm_provider: str = Field(
        default="anthropic", description="LLM provider: anthropic | openai | ollama"
    )
    llm_model: str = Field(
        default="claude-haiku-4-5", description="Model for the configured provider"
    )
    ollama_base_url: str = Field(default="http://localhost:11434", description="Ollama server URL")
    ollama_fallback_model: str = Field(
        default="llama3.2:1b",
        description="Ollama model for the free local fallback (1B fits ~2GB RAM)",
    )

    # Cloud provider API keys (read from env; never logged). Leaving these unset
    # with a cloud llm_provider triggers the automatic Ollama fallback.
    anthropic_api_key: Optional[str] = Field(default=None, description="ANTHROPIC_API_KEY")
    openai_api_key: Optional[str] = Field(default=None, description="OPENAI_API_KEY")

    # Audit pipeline tuning
    verifier_concurrency: int = Field(default=5, description="Max concurrent claim verifications")
    rag_enabled: bool = Field(
        default=True, description="Enable Qdrant retrieval when QDRANT_URL set"
    )

    # gRPC Server Settings
    grpc_port: int = Field(default=50051, description="gRPC server port")
    # 0.0.0.0 is intentional: other containers must reach the engine on the
    # internal Docker network; the port is NOT published to the host (compose).
    grpc_host: str = Field(default="0.0.0.0", description="gRPC server host")  # nosec B104

    # Logging
    log_level: str = Field(default="INFO", description="Log level")

    # Optional: Redis, Qdrant, etc.
    redis_url: Optional[str] = Field(default=None, description="Redis connection URL")
    qdrant_url: Optional[str] = Field(default=None, description="Qdrant server URL")

    # LangSmith tracing (optional - set API key to enable)
    langsmith_api_key: Optional[str] = Field(default=None, description="LangSmith API key")
    langsmith_project: str = Field(default="trustagent", description="LangSmith project name")
    langsmith_tracing: bool = Field(default=False, description="Enable LangSmith tracing")

    class Config:
        env_file = ".env"
        env_file_encoding = "utf-8"
        case_sensitive = False


# Global settings instance
_settings: Optional[Settings] = None


def get_settings() -> Settings:
    """Get or create settings instance."""
    global _settings
    if _settings is None:
        _settings = Settings()
    return _settings
