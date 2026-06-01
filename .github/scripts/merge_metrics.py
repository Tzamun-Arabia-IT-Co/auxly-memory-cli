#!/usr/bin/env python3
"""Merge a daily GitHub metrics snapshot into CSV history.

GitHub's traffic endpoints (clones, views) only retain the last 14 days, so we
*upsert* per-day rows keyed by calendar date — running at least once a fortnight
captures the full timeline with no gaps and no double counting. Release download
counts are cumulative counters, so we keep one snapshot row per run-day to chart
the running total over time.

Reads raw API JSON dumped by the workflow into RAW_DIR and writes CSVs into
METRICS_DIR (both overridable via env so the workflow can target a checkout of
the isolated `metrics` branch).
"""
import csv
import datetime
import json
import os

RAW_DIR = os.environ.get("RAW_DIR", ".metrics-raw")
METRICS_DIR = os.environ.get("METRICS_DIR", "metrics")
os.makedirs(METRICS_DIR, exist_ok=True)


def load(name):
    with open(os.path.join(RAW_DIR, name)) as f:
        return json.load(f)


def upsert_csv(filename, header, new_rows, key):
    """Read existing rows, replace any with a matching key, write back sorted."""
    path = os.path.join(METRICS_DIR, filename)
    existing = {}
    if os.path.exists(path):
        with open(path, newline="") as f:
            reader = csv.reader(f)
            next(reader, None)  # skip header
            for row in reader:
                if row:
                    existing[key(row)] = row
    for row in new_rows:
        existing[key(row)] = row
    with open(path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(header)
        for k in sorted(existing):
            writer.writerow(existing[k])


today = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")

releases = load("releases.json")
repo = load("repo.json")

# --- Daily snapshot: cumulative download total + repo social counters ---
total = sum(a["download_count"] for rel in releases for a in rel["assets"])
upsert_csv(
    "downloads.csv",
    ["date", "total_downloads", "stars", "forks"],
    [[today, str(total), str(repo["stargazers_count"]), str(repo["forks_count"])]],
    key=lambda r: r[0],
)

# --- Per-asset daily snapshot (which install channel pulled what) ---
asset_rows = [
    [today, rel["tag_name"], a["name"], str(a["download_count"])]
    for rel in releases
    for a in rel["assets"]
]
upsert_csv(
    "downloads_by_asset.csv",
    ["date", "tag", "asset", "downloads"],
    asset_rows,
    key=lambda r: (r[0], r[1], r[2]),
)

# --- Clones: upsert each calendar day from the 14-day window ---
clones = load("clones.json").get("clones", [])
upsert_csv(
    "clones.csv",
    ["date", "clones", "unique_cloners"],
    [[c["timestamp"].split("T")[0], str(c["count"]), str(c["uniques"])] for c in clones],
    key=lambda r: r[0],
)

# --- Views: same per-day upsert ---
views = load("views.json").get("views", [])
upsert_csv(
    "views.csv",
    ["date", "views", "unique_visitors"],
    [[v["timestamp"].split("T")[0], str(v["count"]), str(v["uniques"])] for v in views],
    key=lambda r: r[0],
)

print(f"metrics merged for {today}: total_downloads={total} "
      f"stars={repo['stargazers_count']} clone_days={len(clones)} view_days={len(views)}")
