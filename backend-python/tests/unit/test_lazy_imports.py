"""
The engine starts without the knowledge extra (SPEC section 3.2,
test_lazy_imports; the CI job without extras runs `python -c "import
truthtable.main"`): importing the engine must not import torch,
sentence-transformers or qdrant-client, and must work when they are not
installed at all.
"""

import subprocess
import sys
from pathlib import Path

BACKEND_DIR = Path(__file__).resolve().parents[2]

KNOWLEDGE_PACKAGES = ("torch", "sentence_transformers", "qdrant_client")

# Modules that only the knowledge add-on may pull in.
KNOWLEDGE_MODULES = KNOWLEDGE_PACKAGES + ("truthtable.vectorstore", "truthtable.kb")

ENGINE_MODULES = (
    "truthtable.main",
    "truthtable.grpc.server",
    "truthtable.providers",
    "truthtable.config",
    "truthtable.judge",
    "truthtable.engine_info",
    "truthtable.internal_auth",
    "truthtable.healthcheck",
)

# A meta-path finder that makes the knowledge packages uninstallable, like a
# fresh `pip install ./backend-python` without the extra.
_BLOCK_KNOWLEDGE = f"""
import sys

class _NoKnowledgeExtra:
    blocked = {KNOWLEDGE_PACKAGES!r}

    def find_spec(self, name, path=None, target=None):
        if name.split(".")[0] in self.blocked:
            raise ModuleNotFoundError(f"No module named {{name!r}} (knowledge extra not installed)")
        return None

sys.meta_path.insert(0, _NoKnowledgeExtra())
"""


def _run(code: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-c", code],
        env={"PYTHONPATH": str(BACKEND_DIR / "src"), "TRUTHTABLE_ENV_FILE": ""},
        capture_output=True,
        text=True,
        timeout=120,
    )


def test_importing_the_engine_does_not_import_the_knowledge_packages() -> None:
    probe = (
        "import sys\n"
        + "".join(f"import {module}\n" for module in ENGINE_MODULES)
        + f"print(','.join(m for m in {KNOWLEDGE_MODULES!r} if m in sys.modules))\n"
    )

    completed = _run(probe)

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == ""


def test_the_engine_imports_without_the_knowledge_extra_installed() -> None:
    probe = _BLOCK_KNOWLEDGE + (
        "import truthtable.main\n"
        "from truthtable.main import main, init_knowledge\n"
        "try:\n"
        "    import torch\n"
        "except ModuleNotFoundError:\n"
        "    print('blocked')\n"
    )

    completed = _run(probe)

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == "blocked"


def test_knowledge_start_reports_a_missing_extra() -> None:
    probe = _BLOCK_KNOWLEDGE + (
        "import logging\n"
        "from truthtable.config import Settings\n"
        "from truthtable.main import init_knowledge\n"
        "s = Settings(_env_file=None, knowledge_enabled=True, qdrant_url='http://qdrant:6333')\n"
        "*_, state = init_knowledge(s, None, logging.getLogger('t'))\n"
        "print(state.enabled, state.reason.split(':')[0])\n"
    )

    completed = _run(probe)

    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip() == "False extras_missing"
