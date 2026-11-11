"""The worker memory cap, shared by every entry point that runs as its own process (plane_worker.py, analysis.py).

Call limit_memory() once in main(), after the libraries are imported. An allocation over the cap raises MemoryError;
the entry point answers that one request with over_cap_message() and carries on (plane_worker) or exits 2 (analysis).
"""
from __future__ import annotations

import os

DEFAULT_MB = 3072


def cap_mb() -> int:
    """VESQIRA_WORKER_MEM_MB as a number of MB (default 3072); 0 or less disables the cap, an unreadable value means the default."""
    try:
        return int(os.environ.get("VESQIRA_WORKER_MEM_MB", str(DEFAULT_MB)))
    except ValueError:
        return DEFAULT_MB


def over_cap_message() -> str:
    return f"over the memory cap ({cap_mb()} MB)"


def limit_memory() -> None:
    """Cap the process's private memory so one hostile or oversized file cannot take the machine down.

    RLIMIT_DATA counts anonymous and private memory but not read-only file mappings, so memory-mapped cubes of any size
    are unaffected. Importing numpy and astropy already reserves about 2 GB of address space (thread arenas), so the
    budget is added to what the process holds once its libraries are loaded. 0 disables it. Where /proc is not available
    nothing is limited.
    """
    try:
        mb = cap_mb()
        if mb <= 0:
            return
        with open("/proc/self/status") as f:
            held = int(f.read().split("VmData:")[1].split()[0]) * 1024
        import resource
        _, hard = resource.getrlimit(resource.RLIMIT_DATA)
        want = held + (mb << 20)
        resource.setrlimit(resource.RLIMIT_DATA, (want if hard == resource.RLIM_INFINITY else min(want, hard), hard))
    except (OSError, ValueError, IndexError, ImportError):
        pass
