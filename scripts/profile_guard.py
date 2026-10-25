"""Fail closed before a profiling runner can use somebody else's stack."""

import socket
import time


def ensure_ports_free(ports):
    for port in ports:
        try:
            connection = socket.create_connection(("127.0.0.1", port), 0.1)
        except OSError:
            continue
        connection.close()
        raise RuntimeError(
            f"port {port} is in use; profiling requires an isolated fresh stack"
        )


def wait_port(port, process):
    for _ in range(100):
        code = process.poll()
        if code is not None:
            raise RuntimeError(f"child exited with {code} before port {port} was ready")
        try:
            with socket.create_connection(("127.0.0.1", port), 0.1):
                if process.poll() is None:
                    return
        except OSError:
            pass
        time.sleep(0.05)
    raise RuntimeError(f"port {port} not ready; inspect the component log")
