"""让 `python3 -m pytest scripts/tq_relay_scan/` 在任意 cwd 下都能导入同目录模块。

这些脚本按「直接运行」组织（`python3 scripts/tq_relay_scan/scan.py`），
模块之间是平级 import，所以测试目录本身必须进 sys.path。
"""

import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)
