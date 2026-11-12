"""Drives the gateway with the official Python MCP client, the way Claude Desktop would:
discover devices, watch a temperature, switch a fan on, and watch it fall. Writes results/demo.json."""
import asyncio, json, pathlib, sys, time
from mcp.client.client import Client
from mcp.client.stdio import StdioServerParameters

ROOT = pathlib.Path(__file__).resolve().parents[1]


async def main():
    params = StdioServerParameters(command=str(ROOT / "bin" / "gateway"), args=["-sim", "3", "-listen", "127.0.0.1:18832"])
    log, series = [], []
    async with Client(params) as c:
        tools = (await c.list_tools()).tools

        async def call(name, args):
            t0 = time.perf_counter()
            r = await c.call_tool(name, args)
            ms = (time.perf_counter() - t0) * 1000
            text = r.content[0].text
            log.append(dict(tool=name, arguments=args, is_error=r.is_error, ms=ms, result=json.loads(text) if not r.is_error else text))
            return r

        t_start = time.time()
        await asyncio.sleep(2)
        await call("list_devices", {})
        fan_at = None
        for step in range(70):
            r = await c.call_tool("read_sensor", {"device_id": "esp32-01", "sensor": "temperature"})
            series.append((time.time() - t_start, json.loads(r.content[0].text)["value"]))
            if step == 25:
                await call("set_actuator", {"device_id": "esp32-01", "actuator": "fan", "value": True})
                fan_at = time.time() - t_start
            await asyncio.sleep(0.5)
        await call("read_sensor", {"device_id": "esp32-01", "sensor": "temperature", "last_n": 20})
        await call("set_actuator", {"device_id": "esp32-01", "actuator": "led", "value": 300})
        await call("read_sensor", {"device_id": "esp32-07", "sensor": "temperature"})
    json.dump(dict(tools=[dict(name=t.name, description=t.description, annotations=t.annotations.model_dump(exclude_none=True) if t.annotations else None) for t in tools],
                   log=log, series=series, fan_at=fan_at), open(ROOT / "results" / "demo.json", "w"), indent=1)
    for e in log:
        print(e["tool"], e["is_error"], round(e["ms"], 2), str(e["result"])[:160])


asyncio.run(main())
