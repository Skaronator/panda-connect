#!/usr/bin/env python3
"""Check the local deployment. Only a read-only status request is sent."""

import argparse
import json
import os
import re
import sys
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen


def request(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = Request(base + path, data=data)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urlopen(req, timeout=25) as response:
        return response.read()


def check(args):
    html = request("/").decode()
    assets = re.findall(r'(?:src|href)="(/assets/[^\"]+)"', html)
    if not assets:
        raise RuntimeError("The image did not embed the Prusa Connect frontend")
    for path in assets:
        if not request(path):
            raise RuntimeError(f"Empty frontend asset: {path}")
    devices = json.loads(request("/api/v1/devices"))["items"]
    printers = [d for d in devices if d["driver"] == "pandalink" and d["connected"]]
    if not printers:
        raise RuntimeError("No connected PandaLink printer; check docker compose logs")
    required = "A1 mini" if args.require_a1_mini else args.require_model
    if required and not any(d["model"] == required for d in printers):
        raise RuntimeError(f"No connected {required} on pandalink")
    if args.acceptance and not any(d["model"] == "H3C" for d in printers):
        raise RuntimeError("H3C acceptance requires a connected H3C")
    for printer in printers:
        monitor = printer["model"] != "H3C"
        refresh = "core.command.send_state_info"
        if refresh not in printer["capabilities"]:
            raise RuntimeError("The read-only status capability is missing")
        if monitor:
            prefix = f'/api/v1/devices/{printer["id"]}'
            descriptors = json.loads(request(prefix + "/capabilities"))["capabilities"]
            if set(printer["capabilities"]) != {refresh} or {item["key"] for item in descriptors} != {refresh}:
                raise RuntimeError("The read-only profile advertises more than a status refresh")
        state = printer["state"]
        nozzle = "/bambu/active_nozzle_temp"
        if nozzle not in state and not state.get("/bambu/inventory_known"):
            nozzle = "/tools/0/nozzle_temp"
        for path in ["/state", nozzle, "/temperature/bed"]:
            if state.get(path) is None:
                raise RuntimeError(f"Missing live telemetry: {path}")
        if state["/state"] in ("PRINTING", "PAUSED") and state.get("/job/id"):
            if not printer.get("job", {}).get("running"):
                raise RuntimeError("The host closed a job that is still printing or paused")
        reply = json.loads(request(
            f'/api/v1/devices/{printer["id"]}/commands',
            {"verb": refresh},
        ))
        if not reply.get("ok"):
            raise RuntimeError(f"Status refresh failed: {reply}")
        print(json.dumps({
            "device_id": printer["id"], "model": printer["model"],
            "firmware": printer["firmware"], "state": state["/state"],
            "nozzle_c": state[nozzle],
            "bed_c": state["/temperature/bed"],
            "job_id": state.get("/job/id"),
            "progress": state.get("/job/progress"),
            "refresh": "ok",
        }, indent=2))
        if args.acceptance and not monitor:
            acceptance(printer)
    print(f"PASS: {len(assets)} frontend assets and {len(printers)} live printer(s)")


def acceptance(printer):
    prefix = f'/api/v1/devices/{printer["id"]}'
    capabilities = json.loads(request(prefix + "/capabilities"))
    descriptors = {item["key"]: item for item in capabilities["capabilities"]}
    for name, limit in (("nozzle_temperature", 350), ("bed_temperature", 120)):
        verb = "core.command.set_" + name
        arg, = descriptors[verb]["arguments"]
        expected = dict(name=name, type="integer", required=True,
                        minimum=0, maximum=limit, unit="celsius")
        if any(arg.get(key) != value for key, value in expected.items()):
            raise RuntimeError(f"Unexpected temperature descriptor: {name}")
        # Both of these are rejected independently by the driver and the
        # host schema. Never test valid values or state-based rejections here.
        for args, fault in (({}, "ARGUMENT_MISSING"),
                            ({name: str(limit + 1)}, "ARGUMENT_OUT_OF_RANGE")):
            try:
                request(prefix + "/commands", {"verb": verb, "args": args})
            except HTTPError as error:
                code = json.load(error).get("code")
                if error.code == 400 and code == "core.validation/" + fault:
                    continue
                raise
            raise RuntimeError(f"Host accepted invalid input for {verb}")

    for name in ("pause_print", "resume_print", "stop_print"):
        if descriptors["core.command." + name]["arguments"]:
            raise RuntimeError(f"Unexpected arguments for {name}")

    state = printer["state"]
    heads = sum(state.get(f"/tools/{i}/enabled") is True for i in range(9))
    slots = [state.get(f"/bambu/ams/32/slots/{i}/present") for i in range(9)]
    if state.get("/tool_count") != 9 or not state.get("/bambu/inventory_known"):
        raise RuntimeError("Missing H3C rack topology")
    if not all(isinstance(value, bool) for value in slots):
        raise RuntimeError("AMS 3H slot presence is incomplete (requires unit 32)")

    storages = json.loads(request(prefix + "/storages?refresh=true"))
    deadline = time.monotonic() + 15
    while not storages["reported"] and time.monotonic() < deadline:
        time.sleep(0.25)
        storages = json.loads(request(prefix + "/storages"))
    if not storages["reported"] or not any(s["name"] == "sdcard" for s in storages["items"]):
        raise RuntimeError("No verified sdcard storage report")
    path = prefix + "/files?path=sdcard"
    listing = json.loads(request(path + "&refresh=true"))
    deadline = time.monotonic() + 15
    while listing.get("reading") and time.monotonic() < deadline:
        time.sleep(0.25)
        listing = json.loads(request(path))
    if listing.get("reading") or listing.get("fault"):
        raise RuntimeError("Live sdcard listing did not finish successfully")
    print(f"PASS: control descriptors and safe refusals, {heads} enabled rack heads, "
          f"{sum(slots)}/9 occupied AMS slots, {len(listing['items'])} root entries")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--acceptance", action="store_true", help="also run H3C-only acceptance checks")
    model = parser.add_mutually_exclusive_group()
    model.add_argument("--require-model", help="fail unless this model is connected")
    model.add_argument("--require-a1-mini", action="store_true", help="alias for --require-model 'A1 mini'")
    base = os.environ.get("PANDALINK_URL", "http://127.0.0.1:8081").rstrip("/")
    try:
        check(parser.parse_args())
    except Exception as error:
        print(f"FAIL: {error}", file=sys.stderr)
        sys.exit(1)
