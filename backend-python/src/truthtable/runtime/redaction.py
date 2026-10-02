"""
Secret redaction in the engine's logs (SPEC section 7.5, D42).

The engine holds API keys from two places: its own environment (the judge
keys, the internal token, LangSmith) and the gateway's runtime-config pushes.
None of them may reach a log line, including the text of an exception or a
`logger.debug("%s", proto_message)` of a push.

`Redactor` masks every registered secret verbatim, plus the credential shapes
the gateway masks (`backend-go/internal/config/mask.go`): `sk-...` keys,
`Bearer` and `x-api-key:` values, TrustAgent `ta_` keys, the
`x-trustagent-key:` header, the `ta_session` cookie and a `?ticket=` query.

`install_redaction` puts it on every handler of the root logger, not on
loggers: a filter on a logger does not see records that child loggers
propagate, a handler filter sees all of them. The filter

- replaces the record's message with the redacted `record.getMessage()` and
  sets `args=()`, so later formatting cannot reintroduce a secret;
- pre-renders the traceback (`exc_text`) and `stack_info` redacted;

and each handler's formatter is wrapped so that `format`, `formatException`
and `formatStack` output is redacted too.
"""

import logging
import re
import threading
from types import TracebackType
from typing import Iterable, List, Optional, Pattern, Tuple, Type, Union

from pydantic import SecretStr

from ..config import Settings

MASKED = "[redacted]"

# Shorter loaded secrets are not replaced verbatim (they would mask ordinary
# words); the gateway uses the same limit.
MIN_SECRET_LEN = 6

# (pattern, keeps group 1) in the gateway's order (mask.go secretPatterns).
SECRET_PATTERNS: Tuple[Tuple[Pattern[str], bool], ...] = (
    (re.compile(r"sk-[A-Za-z0-9_-]{8,}"), False),
    (re.compile(r"(?i)(bearer\s+)\S+"), True),
    (re.compile(r"(?i)(x-api-key:\s*)\S+"), True),
    (re.compile(r"ta_[A-Za-z0-9_-]{43}"), False),
    (re.compile(r"(?i)(x-trustagent-key:\s*)\S+"), True),
    (re.compile(r"(ta_session=)[^;\s]+"), True),
    (re.compile(r"([?&]ticket=)[^&\s#]+"), True),
)

_ExcInfo = Union[
    Tuple[Type[BaseException], BaseException, Optional[TracebackType]],
    Tuple[None, None, None],
]


def redact_patterns(text: str) -> str:
    """`text` with the credential patterns masked (no registered secrets)."""
    for pattern, keep_prefix in SECRET_PATTERNS:
        text = pattern.sub((r"\g<1>" + MASKED) if keep_prefix else MASKED, text)
    return text


class Redactor:
    """Masks registered secrets and the credential patterns. Thread-safe."""

    def __init__(self, secrets: Iterable[str] = ()) -> None:
        self._lock = threading.Lock()
        self._secrets: List[str] = []
        self.add(*secrets)

    def add(self, *secrets: str) -> None:
        """Register more secrets (blank and too-short values are ignored)."""
        with self._lock:
            known = set(self._secrets)
            for secret in secrets:
                secret = (secret or "").strip()
                if len(secret) >= MIN_SECRET_LEN and secret not in known:
                    known.add(secret)
                    self._secrets.append(secret)
            # Longest first, so a secret containing another is masked whole.
            self._secrets.sort(key=len, reverse=True)

    def __len__(self) -> int:
        with self._lock:
            return len(self._secrets)

    def redact(self, text: str) -> str:
        """`text` with every registered secret and credential pattern masked."""
        if not text:
            return text
        with self._lock:
            secrets = list(self._secrets)
        for secret in secrets:
            if secret in text:
                text = text.replace(secret, MASKED)
        return redact_patterns(text)


def settings_secrets(settings: Settings) -> List[str]:
    """The values of every secret setting the engine loaded (non-empty only)."""
    values: List[str] = []
    for name in Settings.model_fields:
        value = getattr(settings, name, None)
        if isinstance(value, SecretStr):
            secret = value.get_secret_value().strip()
            if secret:
                values.append(secret)
    return values


class RedactingFilter(logging.Filter):
    """A handler filter that redacts each record before it is formatted."""

    def __init__(self, redactor: Redactor) -> None:
        super().__init__()
        self.redactor = redactor

    def filter(self, record: logging.LogRecord) -> bool:
        try:
            message = record.getMessage()
        except Exception:  # a broken format string: keep what can be shown safely
            message = f"{record.msg!s} (unformattable arguments)"
        record.msg = self.redactor.redact(message)
        record.args = ()
        if record.exc_info and not record.exc_text:
            record.exc_text = self.redactor.redact(
                logging.Formatter().formatException(record.exc_info)
            )
        elif record.exc_text:
            record.exc_text = self.redactor.redact(record.exc_text)
        if record.stack_info:
            record.stack_info = self.redactor.redact(record.stack_info)
        return True


class RedactingFormatter(logging.Formatter):
    """Wraps a handler's formatter and redacts everything it renders."""

    def __init__(self, inner: Optional[logging.Formatter], redactor: Redactor) -> None:
        super().__init__()
        self.inner = inner if inner is not None else logging.Formatter(logging.BASIC_FORMAT)
        self.redactor = redactor

    def format(self, record: logging.LogRecord) -> str:
        return self.redactor.redact(self.inner.format(record))

    def formatException(self, ei: _ExcInfo) -> str:  # noqa: N802 (logging API)
        return self.redactor.redact(self.inner.formatException(ei))

    def formatStack(self, stack_info: str) -> str:  # noqa: N802 (logging API)
        return self.redactor.redact(self.inner.formatStack(stack_info))


_process_redactor = Redactor()


def process_redactor() -> Redactor:
    """The engine's redactor: env secrets at startup, pushed secrets as they arrive."""
    return _process_redactor


def install_redaction(
    redactor: Optional[Redactor] = None, root: Optional[logging.Logger] = None
) -> Redactor:
    """
    Redact every record that reaches a handler of the root logger (see the
    module docstring). Idempotent; call it again after adding a handler.
    """
    redactor = redactor if redactor is not None else _process_redactor
    root = root if root is not None else logging.getLogger()
    for handler in root.handlers:
        if not any(
            isinstance(f, RedactingFilter) and f.redactor is redactor for f in handler.filters
        ):
            handler.addFilter(RedactingFilter(redactor))
        formatter = handler.formatter
        if not (isinstance(formatter, RedactingFormatter) and formatter.redactor is redactor):
            handler.setFormatter(RedactingFormatter(formatter, redactor))
    return redactor
