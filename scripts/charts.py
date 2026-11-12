"""Circuit-board styled figures from results/demo.json and results/bench.json."""
import json, pathlib
import numpy as np
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

ROOT = pathlib.Path(__file__).resolve().parents[1]
D = json.load(open(ROOT / "results/demo.json")); B = json.load(open(ROOT / "results/bench.json"))
BOARD, MASK, COPPER, SILK, PAD, LED = "#0b3d2e", "#0f4f3b", "#d4a24c", "#f2f2e6", "#e8c77a", "#ff5a36"


def board(w=10, h=5):
    plt.rcParams.update({"font.family": "DejaVu Sans Mono", "font.size": 11})
    fig, ax = plt.subplots(figsize=(w, h), dpi=150, facecolor=BOARD)
    ax.set_facecolor(MASK)
    for s in ax.spines.values():
        s.set_color(COPPER); s.set_linewidth(2)
    ax.tick_params(colors=SILK); ax.xaxis.label.set_color(SILK); ax.yaxis.label.set_color(SILK); ax.title.set_color(SILK)
    ax.grid(True, color="#1d6b52", lw=0.8, ls=(0, (1, 3)))
    for x, y in [(0.012, 0.03), (0.988, 0.03), (0.012, 0.97), (0.988, 0.97)]:  # mounting holes
        fig.patches.append(plt.Circle((x, y), 0.008, transform=fig.transFigure, color=PAD, zorder=5))
    return fig, ax


def save(fig, name):
    fig.savefig(ROOT / "results" / name, facecolor=fig.get_facecolor(), bbox_inches="tight", pad_inches=0.25)
    plt.close(fig)


# 1. temperature before and after the fan command
t = np.array([p[0] for p in D["series"]]); v = np.array([p[1] for p in D["series"]]); fa = D["fan_at"]
fig, ax = board()
ax.plot(t, v, color=COPPER, lw=2.6, solid_capstyle="round")
ax.scatter(t, v, s=14, color=PAD, zorder=3)
ax.axvline(fa, color=LED, lw=2, ls="--")
ax.annotate("set_actuator(fan = true)\nconfirmed in {:.2f} ms".format(next(e for e in D["log"] if e["tool"] == "set_actuator")["result"]["round_trip_ms"]),
            xy=(fa, v.max()), xytext=(fa + 1.2, v.max() - 0.05), color=LED, fontsize=11, va="top")
before = v[t < fa][-8:].mean(); after = v[-8:].mean()
ax.set_title(f"esp32-01 temperature, read through MCP every 0.5 s: {before:.2f} → {after:.2f} °C after the fan turned on", fontsize=11)
ax.set_xlabel("seconds since start"); ax.set_ylabel("temperature (°C)")
save(fig, "demo_temperature.png")

# 2. latency distributions
fig, ax = board(10, 4.6)
data = [("command round trip\n(gateway → device → ack)", B["set_actuator_samples"]), ]
sa = np.array(B["set_actuator_samples"])
bins = np.linspace(0, np.percentile(sa, 99.5), 50)
ax.hist(sa, bins=bins, color=COPPER, edgecolor=BOARD, alpha=0.95)
for p, lab in [("p50", "p50"), ("p99", "p99")]:
    x = B["set_actuator_ms"][p]; ax.axvline(x, color=LED if p == "p99" else SILK, ls="--", lw=1.6)
    ax.text(x, ax.get_ylim()[1] * 0.92, f" {lab} {x:.3f} ms", color=LED if p == "p99" else SILK, fontsize=10)
ax.set_xlabel("milliseconds"); ax.set_ylabel("commands"); ax.set_title("2,000 acknowledged commands over MQTT (loopback)", fontsize=12)
save(fig, "latency_hist.png")

# 3. throughput
fig, ax = board(10, 4.4)
tp = B["throughput"]; x = np.arange(len(tp))
bars = ax.bar(x, [r["delivered_per_s"] / 1000 for r in tp], color=COPPER, edgecolor=PAD, width=0.6)
for b_, r in zip(bars, tp):
    ax.text(b_.get_x() + b_.get_width() / 2, b_.get_height(), f"{r['delivered_per_s'] / 1000:.0f}k/s", ha="center", va="bottom", color=SILK)
ax.set_xticks(x, [f"{int(r['publishers'])} device" + ("s" if r["publishers"] > 1 else "") for r in tp]); ax.set_ylabel("messages delivered per second (thousands)")
ax.set_title("Broker fan-out: devices flooding telemetry, one subscriber", fontsize=12)
ax.set_ylim(0, max(r["delivered_per_s"] for r in tp) / 1000 * 1.18)
save(fig, "throughput.png")
print(before, after)
