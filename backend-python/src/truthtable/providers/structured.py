"""
Structured outputs: how each provider is asked for JSON that matches a schema.

A CompletionRequest with `response_schema` asks the provider to constrain its
answer to that JSON Schema. Callers still parse and check the answer (ranges
are checked in code), because not every provider or model enforces every
keyword, and a truncated answer (`max_tokens`) is never valid.

| Provider   | Request field                                                                  |
|------------|--------------------------------------------------------------------------------|
| Anthropic  | `output_config: {format: {type: "json_schema", schema}}`                       |
| OpenAI     | `response_format: {type: "json_schema", json_schema: {name, schema, strict}}`  |
| OpenRouter | same as OpenAI (OpenAI-compatible API)                                         |
| Ollama     | `format: <schema>` (Ollama >= 0.5)                                             |
| Mock       | ignores the schema                                                             |

Anthropic parameter [A5], verified 2026-09-28 against the public docs
(platform.claude.com/docs/en/build-with-claude/structured-outputs): structured
outputs are generally available as `output_config.format` with no beta header;
the earlier `output_format` parameter is deprecated and needs the
`structured-outputs-2025-11-13` beta header. The Anthropic schema subset has
no numeric (`minimum`, `maximum`, `multipleOf`), string length or complex array
constraints and requires `additionalProperties: false` on every object, so
`anthropic_schema` removes the unsupported keywords (the caller validates
them) and closes every object.

TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS selects `auto` (default: send it, and if
the API rejects the parameter with a 400 naming it, e.g. an older model, stop
sending it and log once), `on` (always send it; a rejection is an error) or
`off` (never send it; the prompt alone asks for JSON).
"""

import copy
import re
from enum import Enum
from typing import Any, Dict, Mapping

from .errors import INVALID, ProviderError

JsonSchema = Dict[str, Any]


class StructuredOutputsMode(str, Enum):
    """TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS."""

    AUTO = "auto"
    ON = "on"
    OFF = "off"

    @classmethod
    def parse(cls, value: "str | StructuredOutputsMode") -> "StructuredOutputsMode":
        if isinstance(value, StructuredOutputsMode):
            return value
        try:
            return cls(str(value).strip().lower())
        except ValueError:
            raise ValueError(
                f"structured outputs mode must be auto, on or off, not {value!r}"
            ) from None


# Anthropic's request parameter (verified, see the module docstring).
ANTHROPIC_OUTPUT_CONFIG = "output_config"

# Keywords outside the Anthropic structured-outputs schema subset.
ANTHROPIC_UNSUPPORTED_KEYWORDS = frozenset(
    {
        "minimum",
        "maximum",
        "exclusiveMinimum",
        "exclusiveMaximum",
        "multipleOf",
        "minLength",
        "maxLength",
        "maxItems",
        "uniqueItems",
        "minProperties",
        "maxProperties",
        "patternProperties",
        "contains",
        "minContains",
        "maxContains",
    }
)

# Keywords whose value is one subschema, a list of subschemas, or a map of them.
_SUBSCHEMA_KEYWORDS = frozenset({"items", "additionalProperties", "not", "if", "then", "else"})
_SUBSCHEMA_LIST_KEYWORDS = frozenset({"anyOf", "allOf", "oneOf", "prefixItems"})
_SUBSCHEMA_MAP_KEYWORDS = frozenset({"properties", "$defs", "definitions"})

# OpenAI json_schema names: ^[a-zA-Z0-9_-]{1,64}$.
_OPENAI_NAME_INVALID = re.compile(r"[^A-Za-z0-9_-]")


def _anthropic_node(node: Any) -> Any:
    if isinstance(node, list):
        return [_anthropic_node(item) for item in node]
    if not isinstance(node, dict):
        return node
    out: Dict[str, Any] = {}
    for key, value in node.items():
        if key in ANTHROPIC_UNSUPPORTED_KEYWORDS:
            continue
        if key == "minItems" and isinstance(value, int) and value > 1:
            continue  # only 0 and 1 are supported
        if key in _SUBSCHEMA_MAP_KEYWORDS and isinstance(value, dict):
            out[key] = {name: _anthropic_node(sub) for name, sub in value.items()}
        elif key in _SUBSCHEMA_LIST_KEYWORDS and isinstance(value, list):
            out[key] = [_anthropic_node(sub) for sub in value]
        elif key in _SUBSCHEMA_KEYWORDS and isinstance(value, (dict, list)):
            out[key] = _anthropic_node(value)
        else:
            out[key] = copy.deepcopy(value)
    node_type = out.get("type")
    is_object = node_type == "object" or (isinstance(node_type, list) and "object" in node_type)
    if is_object or "properties" in out:
        out["additionalProperties"] = False
    return out


def anthropic_schema(schema: Mapping[str, Any]) -> JsonSchema:
    """
    `schema` reduced to Anthropic's structured-outputs subset: unsupported
    keywords removed and every object closed (`additionalProperties: false`).
    Property names that happen to equal a keyword are kept.
    """
    result = _anthropic_node(dict(schema))
    assert isinstance(result, dict)
    return result


def anthropic_output_config(schema: Mapping[str, Any]) -> JsonSchema:
    """The Anthropic `output_config` request field for `schema`."""
    return {"format": {"type": "json_schema", "schema": anthropic_schema(schema)}}


def openai_schema_name(name: str) -> str:
    """A valid OpenAI json_schema name (letters, digits, _ and -, 1-64 chars)."""
    cleaned = _OPENAI_NAME_INVALID.sub("_", name or "")[:64]
    return cleaned or "output"


def openai_response_format(schema: Mapping[str, Any], name: str = "output") -> JsonSchema:
    """The OpenAI-compatible `response_format` request field for `schema` (strict)."""
    return {
        "type": "json_schema",
        "json_schema": {
            "name": openai_schema_name(name),
            "schema": copy.deepcopy(dict(schema)),
            "strict": True,
        },
    }


def ollama_format(schema: Mapping[str, Any]) -> JsonSchema:
    """The Ollama `format` request field for `schema`."""
    return copy.deepcopy(dict(schema))


def rejects_parameter(error: ProviderError, parameter: str) -> bool:
    """
    Whether `error` is the API refusing `parameter` itself (a 400 that names
    it, e.g. "output_config: Extra inputs are not permitted"), as opposed to
    any other invalid request.
    """
    return error.kind == INVALID and error.status == 400 and parameter.lower() in str(error).lower()
