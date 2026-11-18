"""Create the stack's alert rules in Grafana: `make grafana-alerts`.

Sixteen rules in three evaluation groups, from what the brain keeps in
Postgres (runs, the reply and notice outboxes, the cleanup queues) and what it
logs to Loki. Each group is PUT whole under a fixed uid, so running it again
updates the same rules rather than adding copies; a rule changed in the UI is
put back to what is written here.

The rules stay editable in Grafana (X-Disable-Provenance), so a threshold can
be tuned there first. They name no contact point: an alert goes where the
default notification policy sends it, which is set once in Grafana with the
contact point, since a Slack webhook is a secret this file cannot hold.

Needs, in Grafana: the Openarity folder (the dashboard provisioning makes it),
a Loki datasource (provisioned) and a PostgreSQL datasource on the openarity
database, added by hand with a read-only user.
"""

import base64
import json
import os
import sys
import urllib.error
import urllib.request

URL = os.environ.get("GRAFANA_URL", "http://127.0.0.1:13000").rstrip("/")
PASSWORD = os.environ["GRAFANA_ADMIN_PASSWORD"]
AUTH = "Basic " + base64.b64encode(f"admin:{PASSWORD}".encode()).decode()


def call(method, path, body=None, provenance=True):
    headers = {"Authorization": AUTH, "Content-Type": "application/json"}
    if not provenance:
        headers["X-Disable-Provenance"] = "true"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: {e.code} {e.read().decode()}")


def find(items, what, **match):
    for item in items:
        if all(item.get(k) == v for k, v in match.items()):
            return item["uid"]
    sys.exit(f"Grafana has no {what}; see the top of {__file__}.")


FOLDER = find(call("GET", "/api/folders"), "Openarity folder", title="Openarity")
DATASOURCES = call("GET", "/api/datasources")
PG = find(DATASOURCES, "PostgreSQL datasource", type="grafana-postgresql-datasource")
LOKI = find(DATASOURCES, "Loki datasource", type="loki")


def sql(q):
    return {"refId": "A", "datasourceUid": PG, "relativeTimeRange": {"from": 600, "to": 0},
            "model": {"refId": "A", "datasource": {"type": "grafana-postgresql-datasource", "uid": PG},
                      "editorMode": "code", "format": "table", "rawQuery": True, "rawSql": q,
                      "instant": True}}


def loki(q):
    return {"refId": "A", "datasourceUid": LOKI, "queryType": "instant",
            "relativeTimeRange": {"from": 900, "to": 0},
            "model": {"refId": "A", "datasource": {"type": "loki", "uid": LOKI},
                      "editorMode": "code", "expr": q, "queryType": "instant"}}


def rule(uid, group, title, query, op, threshold, pending, severity, summary, no_data="OK"):
    """One query (A), its last value (B), and the threshold that fires (C)."""
    expr = {"type": "__expr__", "uid": "__expr__"}
    reduce = {"refId": "B", "datasourceUid": "__expr__", "relativeTimeRange": {"from": 0, "to": 0},
              "model": {"refId": "B", "type": "reduce", "expression": "A", "reducer": "last",
                        "datasource": expr}}
    cond = {"refId": "C", "datasourceUid": "__expr__", "relativeTimeRange": {"from": 0, "to": 0},
            "model": {"refId": "C", "type": "threshold", "expression": "B", "datasource": expr,
                      "conditions": [{"evaluator": {"type": op, "params": [threshold]}}]}}
    return {"uid": uid, "orgID": 1, "folderUID": FOLDER, "ruleGroup": group, "title": title,
            "condition": "C", "data": [query, reduce, cond], "for": pending,
            "noDataState": no_data, "execErrState": "Error",
            "labels": {"severity": severity}, "annotations": {"summary": summary}}


C, W, S = "critical-1m", "warning-5m", "cost-15m"

# A count of rows in a bad state needs no pending period when the query already
# waits (older than N minutes, attempts >= 5); one that can spike and clear,
# a rate or a log count, gets one so a single sample does not page.
GROUPS = {
    C: (60, [
        # A delegate is never taken over (its parent decides), so only roots:
        # one stale past the sweep's 90s means the sweep is not running.
        rule("oa-runs-stuck", C, "Runs stuck running", sql(
            "SELECT count(*) AS value FROM runs WHERE status = 'running' AND parent_run_id IS NULL "
            "AND heartbeat_at < now() - interval '3 minutes'"),
            "gt", 0, "1m", "critical", "A run stopped heartbeating and the sweep has not taken it over"),
        # A taken-over run is queued again under its old created_at; the
        # pending minute lets a worker claim it before this fires.
        rule("oa-runs-queued", C, "Runs queued too long", sql(
            "SELECT count(*) AS value FROM runs WHERE status = 'queued' "
            "AND created_at < now() - interval '5 minutes'"),
            "gt", 0, "1m", "critical", "Runs are waiting for a worker; check the worker container"),
        rule("oa-replies-given-up", C, "Replies or notices given up", sql(
            "SELECT (SELECT count(*) FROM replies WHERE status = 'failed' AND created_at > now() - interval '15 minutes') "
            "+ (SELECT count(*) FROM notices WHERE status = 'failed' AND created_at > now() - interval '15 minutes') AS value"),
            "gt", 0, "0s", "critical", "A channel user did not get a reply or notice"),
        rule("oa-reply-backlog", C, "Reply backlog", sql(
            "SELECT count(*) AS value FROM replies WHERE status = 'pending' "
            "AND created_at < now() - interval '10 minutes'"),
            "gt", 0, "0s", "critical", "Replies have been pending for over 10 minutes"),
        # The brain logs only when asked something, so its silence says
        # nothing; Caddy failing to dial it does. A restart refuses for a
        # second, inside the 2m window; the 3m pending lets that pass.
        rule("oa-brain-unreachable", C, "Brain unreachable", loki(
            'sum(count_over_time({service="caddy"} |~ "dial tcp(: lookup brain | [0-9.]+:2112[01]: )" [2m]))'),
             "gt", 0, "3m", "critical", "Caddy cannot reach the brain"),
        # The worker's sweeps log every 15 minutes even when idle, so 30
        # minutes is two missed. Silence is no data rather than 0, so no data
        # is what fires.
        rule("oa-worker-silent", C, "Worker silent", loki('sum(count_over_time({service="worker"}[30m]))'),
             "lt", 1, "5m", "critical", "The worker has logged nothing for 30 minutes", no_data="Alerting"),
        rule("oa-whatsapp-lease", C, "WhatsApp lost its lease", sql(
            "SELECT count(*) AS value FROM whatsapp_accounts WHERE state = 'connected' "
            "AND (held_by IS NULL OR heartbeat_at < now() - interval '2 minutes')"),
            "gt", 0, "2m", "critical", "A connected WhatsApp account is held by no brain"),
    ]),
    W: (300, [
        # A percentage, held at 0 under five finished runs so two failures in
        # a quiet half hour do not read as 40%.
        rule("oa-run-failure-rate", W, "Run failure rate", sql(
            "SELECT CASE WHEN count(*) < 5 THEN 0 "
            "ELSE 100.0 * count(*) FILTER (WHERE status = 'failed') / count(*) END AS value "
            "FROM runs WHERE parent_run_id IS NULL AND finished_at > now() - interval '30 minutes'"),
            "gt", 20, "10m", "warning", "Over 20% of runs failed in the last 30 minutes"),
        rule("oa-graph-sync", W, "Graph sync stuck", sql(
            "SELECT count(*) AS value FROM graph_outbox WHERE created_at < now() - interval '10 minutes'"),
            "gt", 0, "0s", "warning", "The knowledge graph is behind Postgres by over 10 minutes"),
        rule("oa-cleanup-stuck", W, "Cleanup not draining", sql(
            "SELECT (SELECT count(*) FROM deleted_objects WHERE attempts >= 5) "
            "+ (SELECT count(*) FROM deleted_secrets WHERE attempts >= 5) "
            "+ (SELECT count(*) FROM deleted_rooms WHERE attempts >= 5) AS value"),
            "gt", 0, "0s", "warning", "Objects, secrets or sandbox rooms are failing to delete"),
        rule("oa-discovery-failing", W, "Command server discovery failing", sql(
            "SELECT count(*) AS value FROM mcp_servers WHERE last_discovery_error IS NOT NULL"),
            "gt", 0, "15m", "warning", "A command server's tool discovery is failing"),
        rule("oa-error-logs", W, "Error-log rate",
             loki('sum(count_over_time({service=~"brain|worker", level="ERROR"}[5m]))'),
             "gt", 10, "5m", "warning", "The brain or worker is logging errors"),
        rule("oa-webhook-rejected", W, "Webhook signature rejections",
             loki('sum(count_over_time({service=~"brain|worker"} |= "gateway: signature rejected" [15m]))'),
             "gt", 5, "0s", "warning", "Webhooks are arriving with bad signatures"),
        rule("oa-sweep-fallbacks", W, "Sweep fallbacks",
             loki('sum(count_over_time({service=~"brain|worker"} |= "the sweep will" [15m]))'),
             "gt", 5, "10m", "warning", "Enqueuing keeps failing; the sweep is doing the queue's work"),
        rule("oa-infra-crash", W, "Infrastructure crashes",
             loki('sum(count_over_time({service=~"postgres|falkordb|loki"} |~ "(?i)(fatal|panic|out of memory)" [5m]))'),
             "gt", 0, "0s", "warning", "Postgres, FalkorDB or Loki logged a fatal error"),
    ]),
    S: (900, [
        # A placeholder: set it a few times above what a normal hour spends.
        rule("oa-token-spend", S, "Token spend spike", sql(
            "SELECT coalesce(sum(input_tokens + output_tokens), 0) AS value "
            "FROM runs WHERE created_at > now() - interval '1 hour'"),
            "gt", 2000000, "0s", "warning", "Over 2M tokens spent in the last hour"),
    ]),
}

for name, (interval, rules) in GROUPS.items():
    call("PUT", f"/api/v1/provisioning/folder/{FOLDER}/rule-groups/{name}",
         {"title": name, "folderUid": FOLDER, "interval": interval, "rules": rules}, provenance=False)
    print(f"{name:<12} {len(rules)} rules, every {interval}s")
