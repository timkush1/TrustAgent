"""
Which runtime-config push the engine has applied (SPEC section 3.2 `runtime/`, D31, D34).

The gateway pushes `RuntimeConfig{version, epoch, ...}` whenever an admin
changes the runtime settings. `epoch` identifies the gateway's settings store
and `version` counts its writes. The engine keeps the last applied pair in
memory only: a restarted engine starts from nothing and accepts the next push.

A push is applied when it comes from a new epoch (the settings store was
recreated, or another gateway took over) or carries a version above the
applied one. Anything else (a replay, or an older push that lost a race) is a
no-op.
"""

from dataclasses import dataclass


@dataclass
class RuntimeState:
    """The (epoch, version) of the last applied push; ("", 0) before the first."""

    epoch: str = ""
    version: int = 0

    def accepts(self, epoch: str, version: int) -> bool:
        """Whether a push of (epoch, version) is applied (see the module docstring)."""
        return epoch != self.epoch or version > self.version

    def apply(self, epoch: str, version: int) -> bool:
        """Record (epoch, version) when it is accepted; returns whether it was."""
        if not self.accepts(epoch, version):
            return False
        self.epoch, self.version = epoch, version
        return True
