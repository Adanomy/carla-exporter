# Protocol fixtures

Synthetic fixtures derived from upstream C++ layouts, **not live server captures**.
Python is needed only to regenerate them:

```sh
python3 testdata/protocol/generate.py
```

| CARLA | Source revision | World observer ID | Settings fields |
| --- | --- | --- | --- |
| 0.9.12 | [d23f3dc](https://github.com/carla-simulator/carla/tree/d23f3dc1340e47265eeea2b1b33b2d3a2d6d4f42/LibCarla/source/carla) | 14 | 10 |
| 0.9.13 | [a1b37f7](https://github.com/carla-simulator/carla/tree/a1b37f7f1cf34b0f6f77973c469926ea368d1507/LibCarla/source/carla) | 15 | 10 |
| 0.9.14 | [580ae28](https://github.com/carla-simulator/carla/tree/580ae28c9a33f6c0307fd1737d37d2856360fda0/LibCarla/source/carla) | 16 | 10 |
| 0.9.15 | [d7b45c1](https://github.com/carla-simulator/carla/tree/d7b45c1e159e6d13296f7a3a4e8b13e6c2d62c18/LibCarla/source/carla) | 16 | 11 |
| 0.9.16 | [294096e](https://github.com/carla-simulator/carla/tree/294096eb1c38eabf246e4f3a9cdab704e33a7f4c/LibCarla/source/carla) | 17 | 11 |
| 0.10.0 | [ada75f9](https://github.com/carla-simulator/carla/tree/ada75f920642e18cace0a9f85ecf9d4077ddb531/LibCarla/source/carla) | 16 | 11 |

Layout sources: `sensor/SensorRegistry.h`, `sensor/s11n/SensorHeaderSerializer.h`,
`sensor/s11n/EpisodeStateSerializer.h`, `sensor/data/ActorDynamicState.h`,
`rpc/EpisodeSettings.h`, `rpc/VehicleControl.h`, `rpc/Response.h`,
`MsgPackAdaptors.h`, and `streaming/detail/Token.h`.

All releases use a 48-byte sensor header, 36-byte episode header, and 119-byte
actor records. World frames, settings (fixed and variable step), and version
responses have release-specific fixtures. Actor descriptions, map information,
and both stream-token encodings share the `0915` fixtures.
