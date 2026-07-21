// k6 load: POST /v1/transfers with unique idempotency keys, then poll
// GetTransfer until terminal (prompts/M3.md §1).
// Run: k6 run load/k6/transfers.js  (stack must be up: make up)
import http from "k6/http";
import { check, sleep } from "k6";
import { Trend } from "k6/metrics";

const BASE = __ENV.GATEWAY_URL || "http://localhost:8080";
const transferLatency = new Trend("transfer_terminal_latency", true);

export const options = {
  scenarios: {
    transfers: {
      executor: "ramping-vus",
      startVUs: 1,
      stages: [
        { duration: "20s", target: 5 },
        { duration: "40s", target: 10 },
        { duration: "20s", target: 0 },
      ],
    },
  },
  thresholds: {
    // Target from ADR-0016: p99 < 500ms for the synchronous saga.
    http_req_duration: ["p(99)<500"],
    checks: ["rate>0.99"],
  },
};

// login retries through the auth rate limiter so every setup user gets a
// token (see reads.js note).
function provision(email, name) {
  const h = { headers: { "Content-Type": "application/json" } };
  http.post(`${BASE}/v1/auth/register`, JSON.stringify({ email, password: "k6-password-1", name }), h);
  for (let attempt = 0; attempt < 10; attempt++) {
    const login = http.post(`${BASE}/v1/auth/login`, JSON.stringify({ email, password: "k6-password-1" }), h);
    if (login.status === 200) return login.json("access_token");
    sleep(0.3);
  }
  return null;
}

export function setup() {
  const runId = Date.now();
  const users = [];
  // A small pool of senders, one funded account each; receivers reuse the
  // same pool (P2P between pool members).
  for (let i = 0; i < 10; i++) {
    const token = provision(`k6-${runId}-${i}@load.kz`, `K6 User ${i}`);
    if (!token) continue;
    const acc = http.post(`${BASE}/v1/accounts`, JSON.stringify({ currency: "KZT" }), {
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    });
    const accountId = acc.json("id");
    const number = acc.json("number");
    http.post(`${BASE}/v1/topups`, JSON.stringify({
      account_id: accountId, minor_units: 100000000, currency: "KZT",
    }), {
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
        "Idempotency-Key": `k6-topup-${runId}-${i}`,
      },
    });
    users.push({ token, accountId, number });
    sleep(0.15);
  }
  return { users, runId };
}

export default function (data) {
  const me = data.users[(__VU - 1) % data.users.length];
  const to = data.users[__VU % data.users.length];
  const key = `k6-${data.runId}-${__VU}-${__ITER}`;

  const start = Date.now();
  const res = http.post(`${BASE}/v1/transfers`, JSON.stringify({
    type: "P2P",
    from_account_id: me.accountId,
    to_account_number: to.number,
    minor_units: 100,
    currency: "KZT",
  }), {
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${me.token}`,
      "Idempotency-Key": key,
    },
  });

  const created = check(res, {
    "transfer accepted": (r) => r.status === 201 || r.status === 202 || r.status === 429,
  });
  if (!created || res.status === 429) {
    sleep(0.5); // rate-limited — honest client backs off
    return;
  }

  let state = res.json("state");
  const id = res.json("id");
  // Poll to terminal (202-semantics for ambiguous outcomes).
  for (let i = 0; i < 20 && state !== "COMPLETED" && state !== "FAILED"; i++) {
    sleep(0.2);
    const poll = http.get(`${BASE}/v1/transfers/${id}`, {
      headers: { Authorization: `Bearer ${me.token}` },
    });
    if (poll.status === 200) state = poll.json("state");
  }
  transferLatency.add(Date.now() - start);
  check({ state }, { "terminal state reached": (s) => s.state === "COMPLETED" || s.state === "FAILED" });
  sleep(0.3);
}
