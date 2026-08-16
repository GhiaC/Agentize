from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

from app.viewport import (  # noqa: E402
	DEFAULT_QUALITY,
	parse_quality,
	persist,
	preset_from_size,
	load_persisted,
	options,
	resolve_quality,
)


class ViewportPresetsTest(unittest.TestCase):
	def test_named_qualities(self) -> None:
		self.assertEqual(parse_quality("hd").width, 1280)
		self.assertEqual(parse_quality("full_hd").height, 1080)
		self.assertEqual(parse_quality("4k").width, 3840)
		self.assertEqual(parse_quality("1080p").quality, "full_hd")
		self.assertEqual(parse_quality("3840x2160").quality, "4k")

	def test_rejects_unknown_quality(self) -> None:
		with self.assertRaises(ValueError):
			parse_quality("mobile")

	def test_default_and_env_size_mapping(self) -> None:
		self.assertEqual(resolve_quality(None).quality, DEFAULT_QUALITY)
		self.assertEqual(preset_from_size(3840, 2160).quality, "4k")
		self.assertEqual(preset_from_size(1920, 1080).quality, "full_hd")

	def test_persist_round_trip(self) -> None:
		with tempfile.TemporaryDirectory() as raw:
			root = Path(raw)
			preset = parse_quality("hd")
			persist(root, "session-key", preset)
			loaded = load_persisted(root, "session-key", parse_quality("full_hd"))
			self.assertEqual(loaded.quality, "hd")
			payload = json.loads((root / "sessions" / "session-key" / "viewport.json").read_text(encoding="utf-8"))
			self.assertEqual(payload["width"], 1280)

	def test_options_include_three_named_presets(self) -> None:
		qualities = [item["quality"] for item in options()]
		self.assertEqual(qualities, ["hd", "full_hd", "4k"])


if __name__ == "__main__":
	unittest.main()
