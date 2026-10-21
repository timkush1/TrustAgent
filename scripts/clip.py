#!/usr/bin/env python3
"""Copy text to the Windows clipboard from WSL without garbling it (D213; standard library only).

    python3 scripts/clip.py FILE         # make clip FILE=...
    some-command | python3 scripts/clip.py

Plain ``clip.exe`` reads its input in the console's code page, so UTF-8 text such as "27×" or an
em dash arrives garbled. The text goes through ``iconv -f UTF-8 -t UTF-16LE | clip.exe`` instead,
which Windows reads as Unicode. Without ``iconv`` the same UTF-16LE bytes are produced here.
Without ``clip.exe`` (not WSL, or Windows interop off) the text goes to stdout unchanged, so the
command still shows what it would have copied.

Exit status: 0 copied (or printed), 1 the input is not UTF-8 or the copy failed, 2 usage error.
"""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
from pathlib import Path
from typing import BinaryIO, Callable, Optional, Sequence, TextIO

Which = Callable[[str], Optional[str]]


class ClipError(Exception):
    """The text could not be copied; the message says why."""


def _pipe(argv: Sequence[str], data: bytes, clip: str) -> None:
    """``argv`` (the encoder) piped into ``clip``: two processes, as the shell pipe would run."""
    # Fixed argument lists found on PATH, no shell.
    encoder = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE)  # nosec B603
    if encoder.stdout is None or encoder.stdin is None:  # pragma: no cover - PIPE always gives both
        raise ClipError(f"{argv[0]}: no pipe")
    sink = subprocess.Popen([clip], stdin=encoder.stdout)  # nosec B603
    encoder.stdout.close()  # the sink owns the read end now
    try:
        encoder.stdin.write(data)
    finally:
        encoder.stdin.close()
    if encoder.wait() != 0:
        sink.wait()
        raise ClipError(f"{argv[0]} failed (exit {encoder.returncode})")
    if sink.wait() != 0:
        raise ClipError(f"clip.exe failed (exit {sink.returncode})")


def copy(
    data: bytes,
    out: Optional[BinaryIO] = None,
    which: Which = shutil.which,
) -> str:
    """Copy UTF-8 ``data`` to the clipboard; return "clipboard" or "stdout". Raises ClipError."""
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ClipError(f"the input is not UTF-8 ({exc.reason} at byte {exc.start})") from None
    clip = which("clip.exe")
    if not clip:
        sink = out if out is not None else sys.stdout.buffer
        sink.write(data)
        sink.flush()
        return "stdout"
    iconv = which("iconv")
    try:
        if iconv:
            _pipe([iconv, "-f", "UTF-8", "-t", "UTF-16LE"], data, clip)
        else:
            proc = subprocess.run(  # nosec B603 - clip.exe found on PATH, no shell
                [clip], input=text.encode("utf-16-le"), check=False
            )
            if proc.returncode != 0:
                raise ClipError(f"clip.exe failed (exit {proc.returncode})")
    except OSError as exc:
        raise ClipError(str(exc)) from None
    return "clipboard"


def main(
    argv: Optional[Sequence[str]] = None,
    stdin: Optional[BinaryIO] = None,
    stdout: Optional[BinaryIO] = None,
    stderr: Optional[TextIO] = None,
    which: Which = shutil.which,
) -> int:
    err = stderr if stderr is not None else sys.stderr
    parser = argparse.ArgumentParser(prog="clip.py", description=__doc__.split("\n", 1)[0])
    parser.add_argument("file", nargs="?", help="the file to copy (default: stdin)")
    args = parser.parse_args(argv)
    if args.file and args.file != "-":
        try:
            data = Path(args.file).read_bytes()
        except OSError as exc:
            print(f"clip: {exc}", file=err)
            return 2
    else:
        data = (stdin if stdin is not None else sys.stdin.buffer).read()
    try:
        where = copy(data, out=stdout, which=which)
    except ClipError as exc:
        print(f"clip: {exc}", file=err)
        return 1
    chars = len(data.decode("utf-8"))
    if where == "clipboard":
        print(f"clip: copied {chars} characters to the Windows clipboard (UTF-16LE)", file=err)
    else:
        print(f"clip: no clip.exe here, so the {chars} characters went to stdout", file=err)
    return 0


if __name__ == "__main__":
    sys.exit(main())
