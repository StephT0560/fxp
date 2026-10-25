import importlib.util
import unittest
spec=importlib.util.spec_from_file_location('collect_hops','scripts/collect_hops.py')
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class HopTest(unittest.TestCase):
 def test_nearest_rank_does_not_subtract_percentiles(self):
  self.assertEqual(m.percentile([9,1,5],.5),5)
  self.assertEqual(m.percentile([9,1,5],.99),9)
if __name__=='__main__':unittest.main()
