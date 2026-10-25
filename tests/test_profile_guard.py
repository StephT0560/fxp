import importlib.util
import socket
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "profile_guard", Path("scripts/profile_guard.py")
)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class GuardTest(unittest.TestCase):
    def test_occupied_listener_is_rejected(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            with self.assertRaises(RuntimeError):
                m.ensure_ports_free([listener.getsockname()[1]])

    def test_closed_port_is_free(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        m.ensure_ports_free([port])

    def test_exited_child_cannot_become_ready(self):
        class Exited:
            def poll(self):
                return 2

        with self.assertRaises(RuntimeError):
            m.wait_port(0, Exited())


if __name__ == "__main__":
    unittest.main()
