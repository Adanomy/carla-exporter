"""Evaluate dashboard PromQL and preserve its existing visual structure.

Run ``python3 -m unittest discover -s grafana``. PromQL is evaluated by a local
promtool, or Docker when promtool is unavailable. PROMTOOL and PROMTOOL_IMAGE
override the executable and Docker image respectively; no Python packages are
required. The generated promtool input is JSON, a subset of YAML.
"""

import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SOURCE = {"job": "carla", "instance": "sim:9399"}
VEHICLE = {**SOURCE, "episode_id": "101", "actor_id": "7"}
METADATA = {"role_name": "hero", "blueprint_id": "vehicle.test.car"}


def labels(values):
    return "{" + ",".join((f"{key}={json.dumps(value)}" for key, value in sorted(values.items()))) + "}"


def series(metric, values, identity=None, **extra):
    return {
        "series": metric + labels({**(SOURCE if identity is None else identity), **extra}),
        "values": values,
    }


def sample(value, identity=None, keep_blueprint=False, **extra):
    identity = {**(SOURCE if identity is None else identity), **extra}
    identity.pop("episode_id", None)
    if not keep_blueprint:
        identity.pop("blueprint_id", None)
    return {"labels": labels(identity), "value": value}


def vehicle_info(values="1x6", identity=None, **extra):
    return series(
        "carla_vehicle_info", values, VEHICLE if identity is None else identity, **{**METADATA, **extra}
    )


def build_info(values="1x6", identity=None, **extra):
    return series(
        "carla_exporter_build_info",
        values,
        identity,
        **{"version": "test", "revision": "test", "go_version": "go1.27.3", **extra},
    )


def series_source(item):
    pairs = re.finditer('(\\w+)=("(?:[^"\\\\]|\\\\.)*")', item["series"])
    parsed = {match[1]: json.loads(match[2]) for match in pairs}
    return {key: parsed[key] for key in SOURCE}


class DashboardTests(unittest.TestCase):

    @classmethod
    def setUpClass(cls):
        cls.dashboard = json.loads((HERE / "carla-exporter.json").read_text())
        cls.panels = {}

        def collect(panels):
            for panel in panels:
                cls.panels[panel["id"]] = panel
                collect(panel.get("panels", []))

        collect(cls.dashboard["panels"])

    def expression(self, panel_id, ref_id="A", **variables):
        target = next((t for t in self.panels[panel_id]["targets"] if t["refId"] == ref_id))
        expression = target["expr"]
        defaults = {"job": ".*", "instance": ".*", "actor": ".*", "role": ".*"}
        defaults.update(variables)
        for name, value in defaults.items():
            expression = expression.replace("${" + name + ":regex}", value)
        expression = expression.replace("$__rate_interval", "2m").replace("$__range", "5m")
        self.assertNotRegex(expression, "\\$(?:\\{|[A-Za-z_])", "Unexpanded Grafana variable")
        return expression

    def test_xy_uses_current_samples_while_vehicle_history_queries_remain_ranges(self):
        target = next(item for item in self.panels[36]["targets"] if item["refId"] == "A")
        self.assertIs(target["instant"], True)
        self.assertIs(target["range"], False)
        for panel_id in [31, 32, 33, 34, 35, 39, 40, 41, 42, 43, 44]:
            for history in self.panels[panel_id]["targets"]:
                self.assertIs(history["range"], True, (panel_id, history["refId"]))
                self.assertIs(history["instant"], False, (panel_id, history["refId"]))
        filters = next(
            item["options"] for item in self.panels[36]["transformations"] if item["id"] == "filterByValue"
        )
        self.assertEqual(filters["match"], "all")
        self.assertEqual({item["fieldName"] for item in filters["filters"]}, {"x", "y"})
        self.assertTrue(all(item["config"]["id"] == "isNotNull" for item in filters["filters"]))

    def test_promql_regressions(self):
        groups = []

        def case(name, inputs, checks, seed_builds=True):
            inputs = list(inputs)
            if seed_builds:
                sources = {tuple(series_source(item).items()) for item in inputs}
                known = {
                    tuple(series_source(item).items())
                    for item in inputs
                    if item["series"].startswith("carla_exporter_build_info{")
                }
                inputs.extend((build_info(identity=dict(source)) for source in sorted(sources - known)))
            tests = []
            for panel, ref, at, expected, variables, wrapper in checks:
                expression = self.expression(panel, ref, **variables)
                if wrapper:
                    expression = wrapper(expression)
                tests.append({"expr": expression, "eval_time": at, "exp_samples": expected})
            groups.append({"name": name, "interval": "1m", "input_series": inputs, "promql_expr_test": tests})

        def check(panel, expected, ref="A", at="2m", wrapper=None, **variables):
            return (panel, ref, at, expected, variables, wrapper)

        def vehicle_sample(value, identity=None, keep_blueprint=False, **extra):
            return sample(
                value,
                VEHICLE if identity is None else identity,
                keep_blueprint=keep_blueprint,
                **{**METADATA, **extra},
            )

        speed = "carla_vehicle_speed_meters_per_second"
        position = "carla_vehicle_position_meters"
        gauge = "carla_world_vehicles"
        infra_old = {"pod": "old", "node": "node-a", "container": "v1"}
        infra_new = {"pod": "new", "node": "node-b", "container": "v2"}
        case(
            "identical replicas do not duplicate gauges or vehicles",
            [
                series(gauge, "8x6", **infra_old),
                series(gauge, "8x6", **infra_new),
                series(speed, "12x6", VEHICLE, **infra_old),
                series(speed, "12x6", VEHICLE, **infra_new),
                series("carla_vehicle_present", "1x6", VEHICLE, **infra_old),
                series("carla_vehicle_present", "1x6", VEHICLE, **infra_new),
                vehicle_info(**infra_old),
                vehicle_info(**infra_new),
            ],
            [
                check(7, [sample(8)]),
                check(31, [vehicle_sample(12)]),
                check(30, [vehicle_sample(1, keep_blueprint=True)]),
            ],
        )
        case(
            "different gauges become unavailable",
            [series(gauge, "8x6", **infra_old), series(gauge, "9x6", **infra_new)],
            [check(7, [])],
        )
        case(
            "conflicting vehicle values create gaps without a join error",
            [
                series(speed, "12x6", VEHICLE, **infra_old),
                series(speed, "13x6", VEHICLE, **infra_new),
                vehicle_info(),
            ],
            [check(31, [])],
        )
        case(
            "finite value cannot conceal a NaN replica",
            [series(gauge, "8x6", **infra_old), series(gauge, " ".join(["NaN"] * 7), **infra_new)],
            [check(7, [])],
        )
        case(
            "NaN-only observations are unavailable rather than contradictory",
            [series(gauge, " ".join(["NaN"] * 7), **infra_old)],
            [check(7, [])],
        )
        case(
            "stale observations are gaps rather than zeros",
            [series(gauge, "8 stale _ _ _ _")],
            [check(7, [])],
        )
        history_inputs = [
            series(speed, "1 2 stale _ _ 6", VEHICLE, **infra_old),
            series(speed, "_ _ 3 4 stale _", VEHICLE, **infra_new),
            vehicle_info(**infra_old),
            vehicle_info(**infra_new),
        ]
        history_checks = [
            check(31, [vehicle_sample(value)], at=f"{minute}m")
            for minute, value in [(0, 1), (1, 2), (2, 3), (3, 4), (5, 6)]
        ]
        history_checks.append(check(31, [], at="4m"))
        case(
            "rollout preserves one identity throughout history including a real gap",
            history_inputs,
            history_checks,
        )
        case(
            "metadata conflict is detected before filtering roles",
            [
                series(speed, "12x6", VEHICLE),
                series("carla_vehicle_present", "1x6", VEHICLE),
                vehicle_info(pod="old"),
                vehicle_info(role_name="other", pod="new"),
            ],
            [check(31, [], role="hero"), check(30, [], role="hero")],
        )
        case(
            "a conflicting blueprint cannot be selected arbitrarily",
            [
                series(speed, "12x6", VEHICLE),
                vehicle_info(pod="old"),
                vehicle_info(blueprint_id="vehicle.other.car", pod="new"),
            ],
            [check(31, [])],
        )
        sources = [
            (VEHICLE, 5),
            ({**VEHICLE, "instance": "sim-other:9399"}, 7),
            ({**VEHICLE, "job": "other-carla"}, 11),
            ({**VEHICLE, "actor_id": "8"}, 17),
        ]
        inputs = []
        for identity, value in sources:
            inputs.extend([series(speed, f"{value}x6", identity, pod="pod"), vehicle_info(identity=identity)])
        case(
            "sources and actors remain separate even when role names match",
            inputs,
            [check(31, [vehicle_sample(value, identity) for identity, value in sources])],
        )
        next_episode = {**VEHICLE, "episode_id": "102"}
        case(
            "equal observations from two episodes form one logical vehicle series",
            [
                series(speed, "12x6", VEHICLE, **infra_old),
                series(speed, "12x6", next_episode, **infra_new),
                series("carla_vehicle_present", "1x6", VEHICLE, **infra_old),
                series("carla_vehicle_present", "1x6", next_episode, **infra_new),
                vehicle_info(identity=VEHICLE, **infra_old),
                vehicle_info(identity=next_episode, **infra_new),
            ],
            [check(31, [vehicle_sample(12)]), check(30, [vehicle_sample(1, keep_blueprint=True)])],
        )
        case(
            "conflicting observations across episodes remain a gap",
            [
                series(speed, "12x6", VEHICLE),
                series(speed, "13x6", next_episode),
                vehicle_info(identity=VEHICLE),
                vehicle_info(identity=next_episode),
            ],
            [check(31, [])],
        )
        case(
            "a NaN observation in another episode cannot be hidden by a finite value",
            [
                series(speed, "12x6", VEHICLE),
                series(speed, " ".join(["NaN"] * 7), next_episode),
                vehicle_info(identity=VEHICLE),
                vehicle_info(identity=next_episode),
            ],
            [check(31, [])],
        )
        physical = [
            (VEHICLE, METADATA, 12),
            (next_episode, {**METADATA, "role_name": "other"}, 13),
        ]
        inputs = []
        for identity, metadata, value in physical:
            inputs.extend(
                [series(speed, f"{value}x6", identity), vehicle_info(identity=identity, **metadata)]
            )
        case(
            "different roles remain distinct after physical episode joins",
            inputs,
            [
                check(31, [sample(value, identity, **metadata) for identity, metadata, value in physical]),
                check(
                    31,
                    [
                        sample(value, identity, **metadata)
                        for identity, metadata, value in physical
                        if metadata["role_name"] == "hero"
                    ],
                    role="hero",
                ),
            ],
        )
        case(
            "simultaneous blueprints for one logical vehicle are unavailable",
            [
                series(speed, "12x6", VEHICLE),
                series(speed, "12x6", next_episode),
                vehicle_info(identity=VEHICLE),
                vehicle_info(identity=next_episode, blueprint_id="vehicle.other.car"),
            ],
            [check(31, [])],
        )
        case(
            "a descriptive blueprint change preserves a disjoint logical history",
            [
                series(speed, "1 2 stale _ _ _", VEHICLE),
                series(speed, "_ _ 3 4 5 6", next_episode),
                series("carla_vehicle_present", "1 1 stale _ _ _", VEHICLE),
                series("carla_vehicle_present", "_ _ 1 1 1 1", next_episode),
                vehicle_info(values="1 1 stale _ _ _", identity=VEHICLE),
                vehicle_info(values="_ _ 1 1 1 1", identity=next_episode, blueprint_id="vehicle.other.car"),
            ],
            [
                check(31, [vehicle_sample(value)], at=f"{minute}m")
                for minute, value in [(0, 1), (1, 2), (2, 3), (5, 6)]
            ]
            + [
                check(30, [vehicle_sample(1, keep_blueprint=True)], at="0m"),
                check(30, [vehicle_sample(1, keep_blueprint=True, blueprint_id="vehicle.other.car")]),
            ],
        )
        case(
            "metadata from another physical episode cannot label a vehicle observation",
            [
                series(speed, "12x6", VEHICLE),
                series("carla_vehicle_present", "1x6", VEHICLE),
                vehicle_info(identity=next_episode),
            ],
            [check(31, []), check(30, [])],
        )
        inputs = []
        screenshot_cars = []
        for offset, actor_id in enumerate(["197", "198", "199"]):
            identity = {"job": "carla", "instance": "megamozg", "actor_id": actor_id}
            metadata = {"role_name": f"carla-server-0{offset + 1}", "blueprint_id": "vehicle.toyota.prius"}
            old = {**identity, "episode_id": "4793210458869088429"}
            new = {**identity, "episode_id": "8319475967840522193"}
            base = offset * 10
            inputs.extend(
                [
                    vehicle_info(identity=old, **metadata, **infra_old),
                    vehicle_info(identity=new, **metadata, **infra_new),
                    series(speed, f"{base + 1} {base + 2} stale _ _ _", old, **infra_old),
                    series(speed, f"_ _ {base + 3} {base + 4} {base + 5} {base + 6}", new, **infra_new),
                    series("carla_vehicle_present", "1 1 stale _ _ _", old, **infra_old),
                    series("carla_vehicle_present", "_ _ 1 1 1 1", new, **infra_new),
                ]
            )
            screenshot_cars.append((identity, metadata, base))
        case(
            "three observed cars retain one history each across exporter episode changes",
            inputs,
            [
                check(
                    panel,
                    [
                        sample((base + minute + 1) if panel == 31 else 1, identity, **metadata)
                        for identity, metadata, base in screenshot_cars
                    ],
                    at=f"{minute}m",
                )
                for minute in [0, 1, 2, 5]
                for panel in [31, 32]
            ],
        )
        inputs = []
        current_positions = []
        for offset, actor_id in enumerate(["197", "198", "199"]):
            identity = {"job": "carla", "instance": "megamozg", "actor_id": actor_id}
            metadata = {"role_name": f"carla-server-0{offset + 1}", "blueprint_id": "vehicle.toyota.prius"}
            old = {**identity, "episode_id": "4793210458869088429"}
            new = {**identity, "episode_id": "8319475967840522193"}
            inputs.extend([vehicle_info(identity=old, **metadata), vehicle_info(identity=new, **metadata)])
            # The end sample is deliberately different from every earlier
            # coordinate, so returning history cannot satisfy the assertion.
            for axis, direction in [("x", 1), ("y", -1)]:
                values = [direction * (offset * 100 + point) for point in range(1, 7)]
                inputs.extend(
                    [
                        series(position, f"{values[0]} {values[1]} stale _ _ _", old, axis=axis),
                        series(position, "_ _ " + " ".join(map(str, values[2:])), new, axis=axis),
                    ]
                )
                current_positions.append(sample(values[-1], identity, **metadata, axis=axis))
        case(
            "XY instant query returns only three current coordinate pairs at the selected range end",
            inputs,
            [check(36, current_positions, at="5m")],
        )
        case(
            "a stale XY axis remains absent instead of inventing a current coordinate pair",
            [
                vehicle_info(),
                series(position, "1 2 3 4 5 6", VEHICLE, axis="x"),
                series(position, "2 3 4 stale _ _", VEHICLE, axis="y"),
            ],
            [check(36, [vehicle_sample(6, axis="x")], at="5m")],
        )
        inputs = []
        second = {**VEHICLE, "instance": "sim-other:9399"}
        for identity, hand_brake, reverse in [(VEHICLE, 0, 1), (second, 1, 0)]:
            for episode in [identity, {**identity, "episode_id": "102"}]:
                inputs.extend(
                    [
                        vehicle_info(identity=episode),
                        series("carla_vehicle_control_hand_brake", f"{hand_brake}x6", episode),
                        series("carla_vehicle_control_reverse", f"{reverse}x6", episode),
                    ]
                )
        case(
            "hand brake and reverse stay separate for each logical vehicle source",
            inputs,
            [
                check(44, [vehicle_sample(0), vehicle_sample(1, second)]),
                check(44, [vehicle_sample(1), vehicle_sample(0, second)], ref="B"),
            ],
        )
        for panel, metric, value, dimensions in [
            (33, "carla_vehicle_control_throttle_ratio", 0.25, {}),
            (34, "carla_vehicle_control_brake_ratio", 0.5, {}),
            (35, "carla_vehicle_control_steer", -0.25, {}),
            (39, "carla_vehicle_yaw_radians", 1.5, {}),
            (40, "carla_vehicle_control_gear", 2, {}),
            (42, "carla_vehicle_acceleration_meters_per_second_squared", 3, {"axis": "z"}),
            (43, "carla_vehicle_angular_velocity_radians_per_second", -4, {"axis": "y"}),
        ]:
            case(
                f"panel {panel}: logical identity survives an overlapping episode change",
                [
                    vehicle_info(identity=VEHICLE),
                    vehicle_info(identity=next_episode),
                    series(metric, f"{value}x6", VEHICLE, **dimensions, **infra_old),
                    series(metric, f"{value}x6", next_episode, **dimensions, **infra_new),
                ],
                [check(panel, [vehicle_sample(value, **dimensions)])],
            )
        inputs = [vehicle_info(identity=VEHICLE), vehicle_info(identity=next_episode)]
        for identity in [VEHICLE, next_episode]:
            inputs.extend(
                series(position, f"{value}x6", identity, axis=axis)
                for axis, value in [("x", -2), ("y", 0), ("z", 3)]
            )
        axes = [vehicle_sample(value, axis=axis) for axis, value in [("x", -2), ("y", 0), ("z", 3)]]
        case(
            "coordinate queries form one set of axes across an overlapping episode change",
            inputs,
            [check(36, axes[:2]), check(37, axes), check(37, axes[2:], ref="B")],
        )
        selections = {"job": "(carla|other-carla)", "instance": "(sim:9399|sim-other:9399)"}
        selected = [
            (VEHICLE, {**METADATA, "blueprint_id": "vehicle.first.car"}, 5, 1, 10),
            (
                {**VEHICLE, "instance": "sim-other:9399"},
                {**METADATA, "blueprint_id": "vehicle.second.car"},
                7,
                2,
                20,
            ),
            (
                {**VEHICLE, "job": "other-carla"},
                {"role_name": "other", "blueprint_id": "vehicle.third.car"},
                11,
                3,
                30,
            ),
        ]
        unrelated = [
            ({**VEHICLE, "job": "unselected-job"}, METADATA, 13, 4, 40),
            ({**VEHICLE, "instance": "unselected:9399"}, METADATA, 17, 5, 50),
        ]
        inputs = []
        for identity, metadata, value, x, y in selected + unrelated:
            inputs.extend(
                [
                    vehicle_info(identity=identity, **metadata),
                    series("carla_vehicle_present", "1x6", identity),
                    series(speed, f"{value}x6", identity),
                    series(position, f"{x}x6", identity, axis="x"),
                    series(position, f"{y}x6", identity, axis="y"),
                ]
            )
        selected_samples = [
            sample(value, identity, **metadata) for identity, metadata, value, _, _ in selected
        ]
        hero_samples = selected_samples[:2]
        xy_samples = [
            sample(value, identity, **metadata, axis=axis)
            for identity, metadata, _, x, y in selected[:2]
            for axis, value in [("x", x), ("y", y)]
        ]
        case(
            "regex source filters preserve metadata and independent coordinate pairs",
            inputs,
            [
                check(31, selected_samples, **selections),
                check(31, hero_samples, role="hero", **selections),
                check(
                    30,
                    [
                        sample(1, identity, keep_blueprint=True, **metadata)
                        for identity, metadata, _, _, _ in selected[:2]
                    ],
                    role="hero",
                    **selections,
                ),
                check(36, xy_samples, role="hero", **selections),
            ],
        )
        inputs = []
        second = {**VEHICLE, "instance": "sim-other:9399"}
        for identity, multiplier, role in [(VEHICLE, 1, "hero"), (second, 10, "other")]:
            inputs.extend(
                [
                    vehicle_info(identity=identity, role_name=role, **infra_old),
                    vehicle_info(identity=identity, role_name=role, **infra_new),
                    series(speed, f"{multiplier} {2 * multiplier} stale _ _ _", identity, **infra_old),
                    series(
                        speed,
                        f"_ _ {3 * multiplier} {4 * multiplier} {5 * multiplier} {6 * multiplier}",
                        identity,
                        **infra_new,
                    ),
                ]
            )
        case(
            "each selected source keeps its own history across simultaneous pod changes",
            inputs,
            [
                check(
                    31,
                    [
                        vehicle_sample(value, role_name="hero"),
                        vehicle_sample(value * 10, second, role_name="other"),
                    ],
                    at=f"{minute}m",
                    **selections,
                )
                for minute, value in [(0, 1), (2, 3), (5, 6)]
            ],
        )
        inputs = [vehicle_info()]
        for metric in [position, "carla_vehicle_velocity_meters_per_second"]:
            for axis, value in [("x", -2), ("y", 0), ("z", 3)]:
                inputs.extend(
                    [
                        series(metric, f"{value}x6", VEHICLE, axis=axis, **infra_old),
                        series(metric, f"{value}x6", VEHICLE, axis=axis, **infra_new),
                    ]
                )
        axis_samples = [vehicle_sample(value, axis=axis) for axis, value in [("x", -2), ("y", 0), ("z", 3)]]
        case(
            "coordinate and vector axes retain their values and dimensions",
            inputs,
            [check(36, axis_samples[:2]), check(37, axis_samples), check(41, axis_samples)],
        )
        case(
            "missing axes remain absent",
            [
                vehicle_info(),
                series("carla_vehicle_present", "1x6", VEHICLE),
                series(position, "-2x6", VEHICLE, axis="x"),
            ],
            [check(37, [vehicle_sample(-2, axis="x")])],
        )
        case(
            "missing coordinates are not filled with zero",
            [vehicle_info(), series("carla_vehicle_present", "1x6", VEHICLE)],
            [check(37, [])],
        )
        case(
            "old Z samples cannot replace a missing current Z coordinate",
            [
                vehicle_info(),
                series("carla_vehicle_present", "1x6", VEHICLE),
                series(position, "2x6", VEHICLE, axis="x"),
                series(position, "3x6", VEHICLE, axis="y"),
                series(position, "5 stale _ _ _ _", VEHICLE, axis="z"),
            ],
            [
                check(37, [vehicle_sample(5, axis="z")], ref="B", at="0m"),
                check(37, [], ref="B"),
                check(37, [vehicle_sample(2, axis="x"), vehicle_sample(3, axis="y")]),
            ],
        )
        inputs = [vehicle_info()]
        for kind, state, value in [
            ("vehicle", "active", 4),
            ("vehicle", "dormant", 2),
            ("sensor", "active", 3),
            ("sensor", "dormant", 1),
        ]:
            inputs.extend(
                [
                    series("carla_world_actors", f"{value}x6", kind=kind, state=state, **infra_old),
                    series("carla_world_actors", f"{value}x6", kind=kind, state=state, **infra_new),
                ]
            )
        inputs.append(
            series(
                "carla_world_actors",
                "5x6",
                {**SOURCE, "instance": "other:9399"},
                kind="vehicle",
                state="active",
            )
        )
        case(
            "actor sums deduplicate before adding lifecycle states and retain sources",
            inputs,
            [
                check(
                    28,
                    [
                        sample(6, kind="vehicle"),
                        sample(4, kind="sensor"),
                        sample(5, {**SOURCE, "instance": "other:9399"}, kind="vehicle"),
                    ],
                ),
                check(
                    29,
                    [
                        sample(7, state="active"),
                        sample(3, state="dormant"),
                        sample(5, {**SOURCE, "instance": "other:9399"}, state="active"),
                    ],
                ),
            ],
        )
        inputs = []
        for quantile, value in [("0.5", 0.01), ("0.9", 0.05)]:
            inputs.extend(
                [
                    series("go_gc_duration_seconds", f"{value}x6", quantile=quantile, **infra_old),
                    series("go_gc_duration_seconds", f"{value}x6", quantile=quantile, **infra_new),
                ]
            )
        case(
            "GC quantiles remain distinct",
            inputs,
            [check(55, [sample(0.01, quantile="0.5"), sample(0.05, quantile="0.9")])],
        )
        world_metadata = {"map": "Town10", "version": "0.9.16"}
        case(
            "identical world metadata replicas produce a single row",
            [
                series("carla_world_info", "1x6", **world_metadata, **infra_old),
                series("carla_world_info", "1x6", **world_metadata, **infra_new),
            ],
            [check(12, [sample(1, **world_metadata)])],
        )
        case(
            "conflicting world metadata is not selected arbitrarily",
            [
                series("carla_world_info", "1x6", **world_metadata, **infra_old),
                series("carla_world_info", "1x6", map="Town11", version="0.9.16", **infra_new),
            ],
            [check(12, [])],
        )
        case(
            "Go version survives infrastructure deduplication",
            [
                series("go_info", "1x6", version="go1.27.3", **infra_old),
                series("go_info", "1x6", version="go1.27.3", **infra_new),
            ],
            [check(47, [sample(1, version="go1.27.3")])],
        )
        case(
            "contradictory Go versions are metadata conflicts",
            [
                series("go_info", "1x6", version="go1.27.2", **infra_old),
                series("go_info", "1x6", version="go1.27.3", **infra_new),
            ],
            [check(47, [])],
        )
        case(
            "historically known CARLA target remains visible while currently down",
            [
                build_info(values="1 stale _ _ _ _"),
                series("up", "0x6"),
                series("go_goroutines", "7x6"),
                series("process_cpu_seconds_total", "70 90 100 100 100 100"),
            ],
            [check(10, [sample(0)]), check(51, []), check(49, [])],
            seed_builds=False,
        )
        case(
            "failed scrapes use the inventory of the entire selected timeline",
            [build_info(values="_ _ _ 1 1 1"), series("up", "0x6")],
            [
                check(
                    10,
                    [sample(0)],
                    at="4m",
                    wrapper=lambda expression: f"max_over_time(({expression})[1m:1m] @ 60)",
                )
            ],
            seed_builds=False,
        )
        case(
            "historical CARLA ownership cannot claim a different exporter now using the same address",
            [
                build_info(values="1 stale _ _ _ _"),
                series("up", "1x6"),
                series("go_goroutines", "7x6"),
                series("go_info", "1x6", version="go1.27.3"),
                series("process_cpu_seconds_total", "70 90 100 100 100 100"),
            ],
            [check(10, []), check(51, []), check(47, []), check(49, [])],
            seed_builds=False,
        )
        case(
            "build info outside the selected range does not claim an unrelated target",
            [build_info(values="1 stale _ _ _ _ 1 stale _ _ _ _ _"), series("up", "0x12")],
            [check(10, [], at="12m")],
            seed_builds=False,
        )
        case(
            "targets never identified as CARLA do not leak into shared runtime panels",
            [
                series("up", "1x6"),
                series("scrape_duration_seconds", "0.1x6"),
                series("process_cpu_seconds_total", "70 90 100 100 100 100"),
                series("go_goroutines", "7x6"),
            ],
            [check(10, []), check(48, [], ref="B"), check(49, []), check(51, [])],
            seed_builds=False,
        )
        second_source = {"job": "other-carla", "instance": "sim-other:9399"}
        crossed_source = {"job": SOURCE["job"], "instance": second_source["instance"]}
        inputs = [build_info(), build_info(identity=second_source)]
        for identity, up, goroutines, counter in [
            (SOURCE, 0, 3, "80 90 100 100 100 100"),
            (second_source, 1, 4, "60 80 100 100 100 100"),
        ]:
            inputs.extend(
                [
                    series("up", f"{up}x6", identity),
                    series("go_goroutines", f"{goroutines}x6", identity),
                    series("process_cpu_seconds_total", counter, identity),
                ]
            )
        inputs.extend(
            [
                series("up", "1x6", crossed_source),
                series("go_goroutines", "99x6", crossed_source),
                series("process_cpu_seconds_total", "70 90 100 100 100 100", crossed_source, pod="a"),
                series("process_cpu_seconds_total", "60 80 100 100 100 100", crossed_source, pod="b"),
            ]
        )
        case(
            "regex source cross-products cannot introduce non-CARLA source combinations",
            inputs,
            [
                check(10, [sample(0), sample(1, second_source)], **selections),
                check(51, [sample(3), sample(4, second_source)], **selections),
                check(49, [sample(1 / 6), sample(1 / 3, second_source)], **selections),
            ],
            seed_builds=False,
        )
        case(
            "ambiguous build metadata does not conceal numerical conflicts",
            [
                build_info(version="v1", pod="old"),
                build_info(version="v2", pod="new"),
                series("go_goroutines", "3x6", pod="old"),
                series("go_goroutines", "4x6", pod="new"),
            ],
            [check(51, [])],
            seed_builds=False,
        )
        case(
            "actor sums cannot hide one conflicting lifecycle component",
            [
                series("carla_world_actors", "4x6", kind="vehicle", state="active", **infra_old),
                series("carla_world_actors", "5x6", kind="vehicle", state="active", **infra_new),
                series("carla_world_actors", "2x6", kind="vehicle", state="dormant", **infra_old),
                series("carla_world_actors", "3x6", kind="sensor", state="active", **infra_old),
            ],
            [check(28, [sample(3, kind="sensor")]), check(29, [sample(2, state="dormant")])],
        )
        case(
            "rate handles a raw counter reset",
            [series("process_cpu_seconds_total", "0 10 20 5 15 25", pod="same")],
            [check(49, [sample(1 / 6)], at="5m")],
        )
        case(
            "rate follows a replacement without merging raw counters first",
            [
                series("process_cpu_seconds_total", "0 10 20 stale _ _", **infra_old),
                series("process_cpu_seconds_total", "_ _ _ 0 10 20", **infra_new),
            ],
            [check(49, [sample(1 / 12)], at="4m"), check(49, [sample(1 / 6)], at="5m")],
        )
        case(
            "equal current counters can still have conflicting rate histories",
            [
                series("process_cpu_seconds_total", "70 90 100 100 100 100", **infra_old),
                series("process_cpu_seconds_total", "60 80 100 100 100 100", **infra_new),
            ],
            [check(49, [])],
        )
        for panel, metric, step in [
            (26, "carla_world_frames_total", 60),
            (27, "carla_world_simulation_seconds_total", 1),
        ]:
            case(
                f"panel {panel}: selected-range increase of an unambiguous counter",
                [series(metric, f"0+{step}x5", **infra_old)],
                [check(panel, [sample(step * 5)], at="5m")],
            )
            case(
                f"panel {panel}: native NaN input is unavailable rather than ambiguous",
                [series(metric, " ".join(["NaN"] * 7), **infra_old)],
                [check(panel, [], at="5m")],
            )
            case(
                f"panel {panel}: identical overlapping counters are ambiguous history",
                [series(metric, f"0+{step}x5", **infra_old), series(metric, f"0+{step}x5", **infra_new)],
                [check(panel, [], at="5m")],
            )
            case(
                f"panel {panel}: disjoint rollout counters are ambiguous history",
                [
                    series(metric, f"0 {step} {2 * step} stale _ _", **infra_old),
                    series(metric, f"_ _ _ 0 {step} {2 * step}", **infra_new),
                ],
                [check(panel, [], at="5m")],
            )
        case(
            "every saved query parses and absent input does not invent zero series",
            [],
            [
                check(panel["id"], [], ref=target["refId"])
                for panel in self.panels.values()
                for target in panel.get("targets", [])
            ],
        )
        executable = os.environ.get("PROMTOOL") or shutil.which("promtool")
        with tempfile.TemporaryDirectory(prefix="carla-dashboard-tests-") as temporary:
            test_file = Path(temporary) / "tests.json"
            test_file.write_text(json.dumps({"evaluation_interval": "1m", "tests": groups}, indent=2))
            if executable:
                command = [*shlex.split(executable), "test", "rules", str(test_file)]
            else:
                docker = shutil.which("docker")
                self.assertIsNotNone(
                    docker, "Install promtool or Docker to execute dashboard PromQL regressions"
                )
                image = os.environ.get("PROMTOOL_IMAGE", "prom/prometheus:v2.53.4")
                command = [
                    docker,
                    "run",
                    "--rm",
                    "--user",
                    f"{os.getuid()}:{os.getgid()}",
                    "--entrypoint",
                    "/bin/promtool",
                    "-v",
                    f"{temporary}:/work:ro",
                    image,
                    "test",
                    "rules",
                    "/work/tests.json",
                ]
            result = subprocess.run(command, capture_output=True, text=True, timeout=180, check=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
