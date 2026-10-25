import importlib.util
import unittest
import gzip
import json
import pathlib
import tempfile

spec = importlib.util.spec_from_file_location("collect_hops", "scripts/collect_hops.py")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class HopTest(unittest.TestCase):
    def test_nearest_rank_does_not_subtract_percentiles(self):
        self.assertEqual(m.percentile([9, 1, 5], 0.5), 5)
        self.assertEqual(m.percentile([9, 1, 5], 0.99), 9)


class MissingTraceTest(unittest.TestCase):
    def test_empty_trace_is_not_a_success(self):
        with self.assertRaises(ValueError):
            m.collect([])

    def test_incomplete_trace_is_not_silently_dropped(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "incomplete.gz"
            with gzip.open(path, "wt") as stream:
                stream.write(
                    json.dumps(
                        {"id": "run-measured-0", "stage": "client_send", "ns": 1}
                    )
                    + "\n"
                )
            with self.assertRaises(ValueError):
                m.collect([path])


if __name__ == "__main__":
    unittest.main()
