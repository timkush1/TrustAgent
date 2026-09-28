"""
What the engine reports about itself (SPEC section 2.2: GetEngineInfo,
HealthCheck, AuditResult.judge; D10, D12, C-H13).

`EngineContext` is assembled once at startup (settings, the resolved judge,
the knowledge add-on state, configuration warnings); the gRPC servicer adds
live probe results (judge and Qdrant health, knowledge-base size) and turns it
into proto messages with the builders below. Everything here is pure: no I/O.
"""

from dataclasses import dataclass, field
from typing import List, Optional, Tuple

from . import __version__
from .config import Settings
from .grpc.pb import evaluator_pb2 as pb
from .judge import NO_JUDGE, JudgeSelection

# The verifier setting resolves to the LLM judge in 2.0 (the Jev cascade is v2.1).
VERIFIER_LLM = "llm"
VERIFIER_NONE = "none"

# KnowledgeInfo.reason values.
KNOWLEDGE_DISABLED = "disabled"
KNOWLEDGE_NO_QDRANT_URL = "qdrant_url_unset"
KNOWLEDGE_EXTRAS_MISSING = "extras_missing"
KNOWLEDGE_INIT_FAILED = "init_failed"
KNOWLEDGE_UNREACHABLE = "qdrant_unreachable"


@dataclass(frozen=True)
class KnowledgeState:
    """
    The knowledge add-on as started.

    Attributes:
        enabled: Knowledge retrieval is running (store and embeddings loaded).
        embedding_model: The embedding model in use.
        reason: Why it is not running ("" when it is), a KNOWLEDGE_* value,
            optionally followed by ": <detail>".
    """

    enabled: bool = False
    embedding_model: str = ""
    reason: str = KNOWLEDGE_DISABLED


def knowledge_state_for(settings: Settings) -> KnowledgeState:
    """The knowledge state before any start attempt (off, and why)."""
    if not settings.knowledge_enabled:
        return KnowledgeState(reason=KNOWLEDGE_DISABLED)
    if not settings.qdrant_url:
        return KnowledgeState(reason=KNOWLEDGE_NO_QDRANT_URL)
    return KnowledgeState(enabled=True, embedding_model=settings.embedding_model, reason="")


@dataclass
class EngineContext:
    """Startup facts the engine reports (see the module docstring)."""

    settings: Settings
    judge: JudgeSelection
    knowledge: KnowledgeState = field(default_factory=KnowledgeState)
    warnings: List[str] = field(default_factory=list)

    @property
    def max_audits(self) -> int:
        return self.settings.engine_max_audits

    @property
    def max_rpcs(self) -> int:
        return self.settings.engine_max_rpcs

    @property
    def has_judge(self) -> bool:
        return self.judge.effective != NO_JUDGE and self.judge.provider is not None


def judge_info(judge: JudgeSelection, healthy: Optional[bool]) -> "pb.JudgeInfo":
    """JudgeInfo for the resolved judge; `healthy` is the latest probe (None: unknown)."""
    return pb.JudgeInfo(
        configured_provider=judge.configured,
        effective_provider=judge.effective,
        model=judge.model,
        fallback_active=judge.fallback_active,
        fallback_reason=judge.fallback_reason,
        structured_outputs=judge.structured_outputs,
        healthy=bool(healthy) if judge.provider is not None else False,
        credential_source=judge.credential_source,
    )


def scoring_info(settings: Settings) -> "pb.ScoringInfo":
    """ScoringInfo from the TRUTHTABLE_SCORING_* settings (uncalibrated in 2.0)."""
    return pb.ScoringInfo(
        partial_weight=settings.scoring_partial_weight,
        contradiction_flag=settings.scoring_contradiction_flag,
        unsupported_flag=settings.scoring_unsupported_flag,
        unsupported_fraction_flag=settings.scoring_unsupported_fraction_flag,
        min_coverage=settings.scoring_min_coverage,
        max_unknown_fraction=settings.scoring_max_unknown_fraction,
        grade_cutoffs=list(settings.grade_cutoffs),
        unsupported_policy=settings.unsupported_policy,
        calibrated=False,
    )


def knowledge_info(
    state: KnowledgeState, qdrant_reachable: bool, kb_claims: int
) -> "pb.KnowledgeInfo":
    """KnowledgeInfo from the startup state and the latest Qdrant probe."""
    reason = state.reason
    if state.enabled and not qdrant_reachable:
        reason = KNOWLEDGE_UNREACHABLE
    return pb.KnowledgeInfo(
        enabled=state.enabled,
        qdrant_reachable=qdrant_reachable,
        embedding_model=state.embedding_model,
        kb_claims=max(0, kb_claims),
        reason=reason,
    )


def verifier_resolution(settings: Settings, judge: JudgeSelection) -> Tuple[str, str, str]:
    """(configured, effective, reason) of TRUTHTABLE_VERIFIER."""
    configured = settings.verifier
    if judge.provider is None:
        return configured, VERIFIER_NONE, "no judge configured (deep audits are NOT_ASSESSED)"
    if configured == "auto":
        return configured, VERIFIER_LLM, "auto resolves to the LLM judge in 2.0"
    return configured, VERIFIER_LLM, ""


def engine_info(
    context: EngineContext,
    *,
    judge_healthy: Optional[bool],
    qdrant_reachable: bool,
    kb_claims: int,
    runtime_config_version: int = 0,
) -> "pb.EngineInfo":
    """The GetEngineInfo answer."""
    configured, effective, reason = verifier_resolution(context.settings, context.judge)
    warnings = list(context.warnings) + list(context.judge.warnings)
    knowledge = knowledge_info(context.knowledge, qdrant_reachable, kb_claims)
    if context.settings.knowledge_enabled and knowledge.reason:
        warnings.append(f"knowledge add-on enabled but not running: {knowledge.reason}")
    return pb.EngineInfo(
        version=__version__,
        judge=judge_info(context.judge, judge_healthy),
        knowledge=knowledge,
        verifier_configured=configured,
        verifier_effective=effective,
        verifier_reason=reason,
        scoring=scoring_info(context.settings),
        warnings=warnings,
        calibration_version="",
        question_sets=[],
        runtime_config_version=runtime_config_version,
        max_audits=context.max_audits,
        max_rpcs=context.max_rpcs,
    )
