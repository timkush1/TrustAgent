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
    
    # LLM Provider Settings
    llm_provider: str = Field(default="ollama", description="LLM provider to use")
    llm_model: str = Field(default="llama3.2", description="Model name")
    ollama_base_url: str = Field(
        default="http://localhost:11434",
        description="Ollama server URL"
    )
    
    # gRPC Server Settings
    grpc_port: int = Field(default=50051, description="gRPC server port")
    grpc_host: str = Field(default="0.0.0.0", description="gRPC server host")
    
    # Logging
    log_level: str = Field(default="INFO", description="Log level")
    
    # Optional: Redis, Qdrant, etc. (for future use)
    redis_url: Optional[str] = Field(default=None, description="Redis connection URL")
    qdrant_url: Optional[str] = Field(default=None, description="Qdrant server URL")
    
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
