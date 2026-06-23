"""Ensure the project root is importable so `import validator` works from pytest."""

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
