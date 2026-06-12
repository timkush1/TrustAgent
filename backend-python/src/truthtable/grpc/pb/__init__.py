"""Generated gRPC stubs live here (gitignored; regenerated via `make proto`).

grpc_tools emits absolute imports between its own modules
(`import evaluator_pb2`), which fails when the stubs are imported as part of
the `truthtable.grpc.pb` package. Adding this directory to sys.path lets the
generated modules find each other without post-processing the generated code.
"""

import sys
from pathlib import Path

_pb_dir = str(Path(__file__).resolve().parent)
if _pb_dir not in sys.path:
    sys.path.insert(0, _pb_dir)
