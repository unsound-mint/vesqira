#!/usr/bin/env python3
"""Print a VESQIRA_E2E_PORT_BASE whose whole e2e port block (see e2e/ports.ts) is free right now."""
import random
import socket
import sys

OFFSETS = (0, 1, 3, -3589, -3588)  # keep in step with e2e/ports.ts


def free(port: int) -> bool:
    with socket.socket() as s:
        try:
            s.bind(("127.0.0.1", port))
        except OSError:
            return False
    return True


for _ in range(200):
    base = random.randrange(20000, 60000)
    if all(free(base + o) for o in OFFSETS):
        print(base)
        sys.exit(0)
sys.exit("no free e2e port block found")
