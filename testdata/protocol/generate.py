"""Generate synthetic fixtures from the supported CARLA C++ wire layouts.

These are not captures from a live CARLA server. See README.md for source links.
Python is only needed to regenerate fixtures, not to build or run the exporter.
"""
from pathlib import Path
import struct

ROOT = Path(__file__).parent

# Independent of the Go profile registry; see README.md for upstream revisions.
RELEASES = [
    ('0.9.12', '0912', 14, 10),
    ('0.9.13', '0913', 15, 10),
    ('0.9.14', '0914', 16, 10),
    ('0.9.15', '0915', 16, 11),
    ('0.9.16', '0916', 17, 11),
    ('0.10.0', '0100', 16, 11),
]


def pack(value):
    if value is None:
        return b'\xc0'
    if isinstance(value, bool):
        return b'\xc3' if value else b'\xc2'
    if isinstance(value, int):
        if 0 <= value < 128:
            return bytes([value])
        return (b'\xcf' + struct.pack('>Q', value)) if value >= 0 else (b'\xd3' + struct.pack('>q', value))
    if isinstance(value, float):
        return b'\xcb' + struct.pack('>d', value)
    if isinstance(value, str):
        b = value.encode()
        return b'\xdb' + struct.pack('>I', len(b)) + b
    if isinstance(value, bytes):
        return b'\xc6' + struct.pack('>I', len(value)) + value
    if isinstance(value, list):
        return b'\xdc' + struct.pack('>H', len(value)) + b''.join(pack(v) for v in value)
    raise TypeError(type(value))


def actor(actor_id, state, position, yaw, velocity, angular, acceleration, control):
    data = struct.pack('<IB', actor_id, state)
    data += struct.pack('<6f', *position, 0, yaw, 0)
    data += struct.pack('<9f', *velocity, *angular, *acceleration)
    data += control.ljust(54, b'\0')
    assert len(data) == 119
    return data


control = struct.pack('<fff???i', 0.75, -0.25, 0.125, True, True, False, -1)
vehicle = actor(42, 1, (1001.5, -2.25, 3.0), 90.0, (3.0, 4.0, 0.0), (180.0, -90.0, 45.0), (0.1, -0.2, 0.3), control)
sensor = actor(9, 1, (0.0, 0.0, 0.0), 0.0, (0.0, 0.0, 0.0), (0.0, 0.0, 0.0), (0.0, 0.0, 0.0), b'')

for version, suffix, observer_type, settings_fields in RELEASES:
    header = struct.pack('<QQd6f', observer_type, 120, 6.0, *([0.0] * 6))
    header += struct.pack('<QdfiiiI', 987654321, 4567.0, 0.05, 1000, 2000, 0, 0)
    assert len(header) == 48 + 36
    (ROOT / f'world_{suffix}.bin').write_bytes(header + vehicle + sensor)
    settings = [True, False, [True, 0.05], True, 0.01, 10, 0.0, True, 3000.0, 2000.0]
    if settings_fields == 11:
        settings.append(True)
    assert len(settings) == settings_fields
    (ROOT / f'settings_{suffix}.msgpack').write_bytes(pack(settings))
    settings[0] = False
    settings[2] = [False]
    (ROOT / f'settings_variable_{suffix}.msgpack').write_bytes(pack(settings))
    (ROOT / f'version_response_{suffix}.msgpack').write_bytes(pack([1, 1, None, [[1, version]]]))

# These RPC layouts are shared by all supported releases.
(ROOT / 'map_0915.msgpack').write_bytes(pack(['Carla/Maps/Town01', []]))
bbox = [[0.0] * 3, [1.0] * 3, [0.0] * 3]
actors = [[42, 0, [1, 'vehicle.tesla.model3', [['role_name', 3, 'hero']]], bbox, [10], []], [9, 42, [2, 'sensor.camera.rgb', []], bbox, [], []]]
(ROOT / 'actors_0915.msgpack').write_bytes(pack(actors))
token = struct.pack('<IHBB16s', 7, 2001, 1, 0, b'\0' * 16)
(ROOT / 'episode_array_0915.msgpack').write_bytes(pack([987654321, [list(token)]]))
(ROOT / 'episode_bin_0915.msgpack').write_bytes(pack([987654321, [token]]))
