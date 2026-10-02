"""
The engine's judge under runtime configuration (SPEC section 3.2 `runtime/`, P4-PLAN D34, D39, D78).

At startup the engine resolves its judge from its own environment
(truthtable.judge). Afterwards the gateway may push a judge chosen on the
dashboard (`RuntimeConfig.judge`, with the credential it is bound to).
`ProviderManager.apply` decides which judge runs and installs it in the
engine's `SwappableProvider`:

- **The environment wins.** When LLM_PROVIDER is set, or the startup
  resolution found an API key in the engine's environment, the pushed judge
  is ignored (`env_managed`). A pushed setting is never combined with an env
  credential.
- **A push without a judge reverts** to the startup (environment) judge, so no
  later call uses the dashboard credential (`no_judge`). `none` pushes "no
  judge" explicitly (`no_judge` as well).
- **A pushed judge** (`anthropic`, `openrouter`, `openai` with their bound
  credential; `ollama`, or `mock` for simulated checks, without one) is built
  without any call (`configured`), or kept when nothing about it changed
  (`unchanged`). The credential's allowed origins are intersected with the
  engine's own table (runtime/origins.py), so a push can never widen where a
  secret goes.
- **A push the engine cannot use** (an unknown provider, a missing or
  mismatched credential, no allowed origin left) reverts to the environment
  judge like a push without one, and reports `invalid` with the reason.

A replaced judge finishes its running calls before it is closed (drain, then
close). LLM_MODEL, when set, is the model of any judge the engine runs.
"""

import logging
from dataclasses import dataclass, field
from typing import Any, Dict, List, Mapping, Optional, Tuple

from ..config import Settings
from ..grpc.pb import evaluator_pb2 as pb
from ..judge import (
    CREDENTIAL_DASHBOARD,
    CREDENTIAL_ENV,
    CREDENTIAL_NONE,
    KEYED_PROVIDERS,
    MOCK,
    NO_JUDGE,
    OLLAMA,
    JudgeSelection,
    build_mock_judge,
    judge_model,
)
from ..providers.base import LLMProvider
from ..providers.registry import get_provider
from ..providers.swappable import SwappableProvider
from .origins import canonical_origin, canonical_origins, engine_origins, provider_base_url
from .redaction import Redactor, process_redactor

logger = logging.getLogger(__name__)

# ApplyRuntimeConfigResponse.judge_status values (D34).
CONFIGURED = "configured"
UNCHANGED = "unchanged"
ENV_MANAGED = "env_managed"
NO_JUDGE_STATUS = "no_judge"
INVALID = "invalid"

# Providers a push may name with a credential (judge.KEYED_PROVIDERS), and without one.
KEYLESS_PROVIDERS = (OLLAMA, MOCK)

# Engine registry keys that pin the judge when set non-empty, reported by name
# in EngineInfo.env_managed (D34, D80): (registry name, Settings field).
ENV_JUDGE_KEYS: Tuple[Tuple[str, str], ...] = (
    ("LLM_PROVIDER", "llm_provider"),
    ("LLM_MODEL", "llm_model"),
    ("ANTHROPIC_API_KEY", "anthropic_api_key"),
    ("OPENAI_API_KEY", "openai_api_key"),
    ("OPENROUTER_API_KEY", "openrouter_api_key"),
)


class PushRejected(ValueError):
    """The pushed judge cannot be used (judge_status `invalid`)."""


@dataclass(frozen=True)
class JudgeKey:
    """What identifies a built judge: a different key means a rebuild."""

    provider: str
    model: str
    base_url: str = ""
    origins: Tuple[str, ...] = ()
    fingerprint: str = ""
    # Compared (a rotated secret with a stale fingerprint still rebuilds), never shown.
    secret: str = field(default="", repr=False)


@dataclass(frozen=True)
class ApplyOutcome:
    """The judge part of one applied push."""

    judge_status: str
    warnings: Tuple[str, ...] = ()
    rebuilt: bool = False


def _value_set(settings: Settings, field_name: str) -> bool:
    if field_name not in settings.model_fields_set:
        return False
    value: Any = getattr(settings, field_name)
    if hasattr(value, "get_secret_value"):
        value = value.get_secret_value()
    return bool(str(value).strip())


def env_managed_keys(settings: Settings) -> List[str]:
    """Names of the judge registry keys set non-empty in the engine's env (names only)."""
    return [name for name, field_name in ENV_JUDGE_KEYS if _value_set(settings, field_name)]


class ProviderManager:
    """
    Applies the judge part of runtime-config pushes (see the module docstring).

    Args:
        settings: Engine settings (the env side of every decision).
        env_selection: The judge resolved from the environment at startup.
        swappable: The engine's judge slot; it holds env_selection's judge.
        redactor: Where pushed secrets are registered before anything else
            happens with them (default: the process redactor).
        provider_options: Extra constructor arguments of the HTTP providers
            (tests pass an httpx transport).
    """

    def __init__(
        self,
        settings: Settings,
        env_selection: JudgeSelection,
        swappable: SwappableProvider,
        *,
        redactor: Optional[Redactor] = None,
        provider_options: Optional[Mapping[str, Any]] = None,
    ) -> None:
        self.settings = settings
        self.env_selection = env_selection
        self.swappable = swappable
        self.redactor = redactor if redactor is not None else process_redactor()
        self.provider_options: Dict[str, Any] = dict(provider_options or {})
        self._selection = env_selection
        self._key: Optional[JudgeKey] = None  # None: the environment judge is installed
        self.warnings: Tuple[str, ...] = ()
        if swappable.current is not env_selection.provider:
            swappable.swap(env_selection.provider)

    # -- what the engine reports ------------------------------------------

    @property
    def selection(self) -> JudgeSelection:
        """The judge in use (JudgeInfo)."""
        return self._selection

    @property
    def has_judge(self) -> bool:
        return self._selection.provider is not None and self._selection.effective != NO_JUDGE

    @property
    def env_pinned(self) -> bool:
        """Whether the environment decides the judge (pushed judges are ignored)."""
        if _value_set(self.settings, "llm_provider"):
            return True
        if self.env_selection.credential_source == CREDENTIAL_ENV:
            return True
        return self.settings.demo_mode and self.env_selection.effective == MOCK

    def env_managed(self) -> List[str]:
        """EngineInfo.env_managed: names of the judge keys set in the engine's env."""
        return env_managed_keys(self.settings)

    # -- applying a push ---------------------------------------------------

    async def apply(self, config: "pb.RuntimeConfig") -> ApplyOutcome:
        """Apply the judge of an accepted push (see the module docstring). Never calls the judge."""
        # Every secret received joins the redactor before anything can log it.
        self.redactor.add(*(c.secret for c in config.credentials.values()))
        pushed = config.judge if config.HasField("judge") else None
        provider = pushed.provider.strip().lower() if pushed is not None else ""

        if self.env_pinned:
            outcome = ApplyOutcome(ENV_MANAGED, self._env_pinned_warnings(provider))
            self.warnings = outcome.warnings
            return outcome
        if pushed is None or not provider:
            rebuilt = self._install_env()
            self.warnings = ()
            return ApplyOutcome(NO_JUDGE_STATUS, rebuilt=rebuilt)
        if provider == NO_JUDGE:
            rebuilt = self._install(
                JudgeKey(NO_JUDGE, ""),
                JudgeSelection.no_judge(
                    NO_JUDGE, "the dashboard sets no judge: deep audits are NOT_ASSESSED (no_judge)"
                ),
            )
            self.warnings = ()
            return ApplyOutcome(NO_JUDGE_STATUS, rebuilt=rebuilt)

        warnings: List[str] = []
        try:
            key = self._key_for(config, pushed, provider, warnings)
            if key == self._key:
                self.warnings = tuple(warnings)
                return ApplyOutcome(UNCHANGED, tuple(warnings))
            judge = self._build(key)
        except PushRejected as exc:
            reason = f"pushed judge not applied: {exc}; using the engine's own judge settings"
            logger.warning(self.redactor.redact(reason))
            warnings.append(reason)
            rebuilt = self._install_env()
            self.warnings = tuple(warnings)
            return ApplyOutcome(INVALID, tuple(warnings), rebuilt=rebuilt)

        keyed = provider in KEYED_PROVIDERS
        selection = JudgeSelection(
            provider=judge,
            configured=provider,
            effective=provider,
            model=key.model,
            credential_source=CREDENTIAL_DASHBOARD if keyed else CREDENTIAL_NONE,
        )
        self._install(key, selection)
        self.warnings = tuple(warnings)
        source = f"dashboard credential {key.fingerprint or '(no fingerprint)'}" if keyed else ""
        logger.info(f"Judge: {provider} (model {key.model}) from the dashboard {source}".rstrip())
        return ApplyOutcome(CONFIGURED, tuple(warnings), rebuilt=True)

    def _env_pinned_warnings(self, provider: str) -> Tuple[str, ...]:
        if not provider:
            return ()
        names = ", ".join(self.env_managed()) or "an API key in the engine's environment"
        return (f"pushed judge {provider} ignored: the engine's environment sets it ({names})",)

    def _key_for(
        self,
        config: "pb.RuntimeConfig",
        pushed: "pb.JudgeConfig",
        provider: str,
        warnings: List[str],
    ) -> JudgeKey:
        if pushed.base_url.strip():
            warnings.append("judge.base_url ignored: the engine calls its own provider endpoints")
        model = (
            self.settings.llm_model.strip()
            or pushed.model.strip()
            or (judge_model(self.settings, provider) if provider in _DEFAULT_MODELS else "")
        )
        if provider in KEYLESS_PROVIDERS:
            if pushed.credential.strip():
                warnings.append(f"judge.credential ignored: {provider} takes no credential")
            base_url = self.settings.ollama_base_url if provider == OLLAMA else ""
            return JudgeKey(provider, model, base_url=base_url)
        if provider not in KEYED_PROVIDERS:
            raise PushRejected(f"unknown judge provider {provider!r}")

        name = pushed.credential.strip()
        if not name:
            raise PushRejected(f"{provider} needs a credential and none is bound")
        if name not in config.credentials:
            raise PushRejected(f"credential {name!r} is not in the push")
        credential = config.credentials[name]
        if credential.provider.strip().lower() != provider:
            raise PushRejected(
                f"credential {name!r} belongs to {credential.provider or 'no provider'}, "
                f"not {provider}"
            )
        secret = credential.secret.strip()
        if not secret:
            raise PushRejected(f"credential {name!r} has no secret")

        pushed_origins, rejected = canonical_origins(credential.allowed_hosts)
        if rejected:
            warnings.append(f"{len(rejected)} malformed allowed origin(s) of {name!r} ignored")
        own = engine_origins(provider, self.settings)
        foreign = [o for o in pushed_origins if o not in own]
        if foreign:
            warnings.append(
                f"allowed origin(s) {', '.join(foreign)} of {name!r} ignored: not a {provider} "
                "origin of this engine"
            )
        origins = tuple(o for o in pushed_origins if o in own)
        if not origins:
            raise PushRejected(f"credential {name!r} has no allowed origin this engine calls")
        base_url = provider_base_url(provider, self.settings)
        try:
            base_origin = canonical_origin(base_url)
        except ValueError:
            # Userinfo or a bad port in the engine's own URL: never send a key there.
            raise PushRejected(
                f"the engine's {provider} URL is not a valid origin; fix its environment"
            ) from None
        if base_origin not in origins:
            raise PushRejected(f"credential {name!r} may not be sent to {base_origin}")
        return JudgeKey(
            provider,
            model,
            base_url=base_url,
            origins=origins,
            fingerprint=credential.fingerprint,
            secret=secret,
        )

    def _build(self, key: JudgeKey) -> LLMProvider:
        """Construct the judge for `key` (no network call)."""
        options = dict(self.provider_options)
        try:
            if key.provider == MOCK:
                return build_mock_judge(self.settings, key.model)
            if key.provider == OLLAMA:
                return get_provider(OLLAMA, model=key.model, base_url=key.base_url, **options)
            if key.provider == "anthropic":
                options["structured_outputs"] = self.settings.anthropic_structured_outputs
            elif key.provider == "openrouter":
                options.update(
                    app_url=self.settings.jev_app_url, app_title=self.settings.jev_app_title
                )
            return get_provider(
                key.provider,
                model=key.model,
                api_key=key.secret,
                base_url=key.base_url,
                allowed_origins=key.origins,
                **options,
            )
        except (OSError, ValueError) as exc:
            raise PushRejected(f"the {key.provider} judge cannot be built ({exc})") from exc

    def _install(self, key: JudgeKey, selection: JudgeSelection) -> bool:
        """Install a pushed judge; returns whether the installed judge changed."""
        changed = self.swappable.current is not selection.provider
        self.swappable.swap(selection.provider)
        self._key, self._selection = key, selection
        return changed

    def _install_env(self) -> bool:
        """Go back to the environment judge; returns whether that changed anything."""
        if self._key is None:
            return False
        self.swappable.swap(self.env_selection.provider)
        self._key, self._selection = None, self.env_selection
        logger.info(f"Judge: back to the engine's own settings ({self.env_selection.effective})")
        return True


# Providers with a built-in default model (judge.judge_model).
_DEFAULT_MODELS = frozenset({*KEYED_PROVIDERS, *KEYLESS_PROVIDERS})
