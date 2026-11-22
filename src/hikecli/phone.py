"""Phone number helpers. Hike's OTP login wants the dial code and national number separately."""

from __future__ import annotations

import re

# Dial codes offered by Hike Web's country picker. Caribbean NANP codes such as 1268
# are listed separately there, so the longest matching code wins.
_DIAL_CODES = frozenset(
    "1,7,20,27,30,31,32,33,34,36,39,40,41,43,44,45,46,47,48,49,51,52,53,54,55,56,57,58,"
    "60,61,62,63,64,65,66,81,82,84,86,90,91,92,93,94,95,98,211,212,213,216,218,220,221,"
    "222,223,224,225,226,227,228,229,230,231,232,233,234,235,236,237,238,239,240,241,242,"
    "243,244,245,246,248,249,250,251,252,253,254,255,256,257,258,260,261,262,263,264,265,"
    "266,267,268,269,291,297,298,299,350,351,352,353,354,355,356,357,358,359,370,371,372,"
    "373,374,375,376,377,378,380,381,382,383,385,386,387,389,420,421,423,501,502,503,504,"
    "505,506,507,508,509,590,591,592,593,594,595,596,597,598,599,670,673,674,675,676,677,"
    "678,679,680,681,685,686,687,688,689,691,692,850,852,853,855,856,880,886,960,961,962,"
    "963,964,965,966,967,968,970,971,972,973,974,975,976,977,992,993,994,995,996,998,1242,"
    "1246,1268,1473,1671,1758,1767,1784,1868,1869,1876".split(",")
)

_PHONE_CHARS = re.compile(r"^\+?[\d\s().-]+$")


def looks_like_phone(value: str) -> bool:
    digits = re.sub(r"\D", "", value)
    return bool(_PHONE_CHARS.match(value.strip())) and 7 <= len(digits) <= 15


def normalize_e164(value: str) -> str | None:
    """Return +<digits> for numbers written with a leading +, else None."""
    value = value.strip()
    if not value.startswith("+"):
        return None
    digits = re.sub(r"\D", "", value)
    return f"+{digits}" if 7 <= len(digits) <= 15 else None


def split_phone(value: str) -> tuple[str, str]:
    """Split '+91 98765 43210' or '+919876543210' into ('+91', '9876543210')."""
    value = value.strip()
    if not value.startswith("+"):
        raise ValueError("phone number must start with + and the country code, e.g. +919876543210")
    parts = value[1:].split()
    if len(parts) > 1 and parts[0].isdigit() and parts[0] in _DIAL_CODES:
        national = re.sub(r"\D", "", "".join(parts[1:]))
        if national:
            return f"+{parts[0]}", national
    digits = re.sub(r"\D", "", value)
    for size in (4, 3, 2, 1):
        code = digits[:size]
        if code in _DIAL_CODES and len(digits) - size >= 4:
            return f"+{code}", digits[size:]
    raise ValueError(f"unknown country code in {value!r}; write it as '+<code> <number>'")
