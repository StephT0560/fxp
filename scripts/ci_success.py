# SPDX-License-Identifier: MIT
"""Fail closed on the complete GitHub Actions needs context (JSON on stdin)."""
import json
import sys

needs = json.load(sys.stdin)
if not isinstance(needs, dict) or not needs:
    sys.exit('No dependency results received')
failed = [name for name, job in needs.items()
          if not isinstance(job, dict) or job.get('result') != 'success']
if failed:
    sys.exit('CI dependencies did not succeed: ' + ', '.join(failed))
print('All CI dependencies succeeded')
