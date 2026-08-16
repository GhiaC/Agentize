from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from pathlib import Path

log = logging.getLogger("browser-use.viewport")

DEFAULT_QUALITY = "full_hd"


@dataclass(frozen=True)
class ViewportPreset:
	quality: str
	width: int
	height: int
	label: str

	def as_dict(self) -> dict[str, object]:
		return {
			"quality": self.quality,
			"width": self.width,
			"height": self.height,
			"label": self.label,
		}


PRESETS: tuple[ViewportPreset, ...] = (
	ViewportPreset("hd", 1280, 720, "HD"),
	ViewportPreset("full_hd", 1920, 1080, "Full HD"),
	ViewportPreset("4k", 3840, 2160, "4K"),
)
PRESET_BY_QUALITY = {item.quality: item for item in PRESETS}
_ALIASES = {
	"720p": "hd",
	"1280x720": "hd",
	"1080p": "full_hd",
	"fullhd": "full_hd",
	"full-hd": "full_hd",
	"1920x1080": "full_hd",
	"uhd": "4k",
	"uhd_4k": "4k",
	"2160p": "4k",
	"3840x2160": "4k",
}


def parse_quality(raw: str) -> ViewportPreset:
	value = str(raw or "").strip().lower().replace(" ", "_")
	value = _ALIASES.get(value, value)
	if value not in PRESET_BY_QUALITY:
		raise ValueError("viewport quality must be hd, full_hd, or 4k")
	return PRESET_BY_QUALITY[value]


def resolve_quality(raw: str | None, default: str = DEFAULT_QUALITY) -> ViewportPreset:
	value = str(raw or "").strip().lower().replace(" ", "_")
	value = _ALIASES.get(value, value)
	if value in PRESET_BY_QUALITY:
		return PRESET_BY_QUALITY[value]
	fallback = default if default in PRESET_BY_QUALITY else DEFAULT_QUALITY
	return PRESET_BY_QUALITY[fallback]


def preset_from_size(width: int, height: int) -> ViewportPreset:
	best = PRESET_BY_QUALITY[DEFAULT_QUALITY]
	best_delta = abs(int(width) * int(height) - best.width * best.height)
	for preset in PRESETS:
		delta = abs(int(width) * int(height) - preset.width * preset.height)
		if delta < best_delta:
			best = preset
			best_delta = delta
	return best


def options() -> list[dict[str, object]]:
	return [item.as_dict() for item in PRESETS]


def viewport_state_path(root: Path, session_key: str) -> Path:
	return root / "sessions" / session_key / "viewport.json"


def load_persisted(root: Path, session_key: str, default: ViewportPreset) -> ViewportPreset:
	path = viewport_state_path(root, session_key)
	try:
		payload = json.loads(path.read_text(encoding="utf-8"))
	except (OSError, json.JSONDecodeError):
		return default
	if not isinstance(payload, dict):
		return default
	quality = str(payload.get("quality", "")).strip()
	if quality:
		return resolve_quality(quality, default.quality)
	width = int(payload.get("width") or 0)
	height = int(payload.get("height") or 0)
	if width > 0 and height > 0:
		return preset_from_size(width, height)
	return default


def persist(root: Path, session_key: str, preset: ViewportPreset) -> None:
	path = viewport_state_path(root, session_key)
	path.parent.mkdir(parents=True, exist_ok=True)
	path.write_text(json.dumps(preset.as_dict(), ensure_ascii=True), encoding="utf-8")
	log.info("viewport persisted session_key=%s quality=%s %sx%s", session_key[:12], preset.quality, preset.width, preset.height)
