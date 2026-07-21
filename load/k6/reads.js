// k6 load: GET /v1/accounts (projected balances) — prompts/M3.md §1.
// Run: k6 run load/k6/reads.js
import http from "k6/http";
import { check, sleep } from "k6";

const BASE = __ENV.GATEWAY_URL || "http://localhost:8080";

export const options = {
  scenarios: {
    reads: {
      executor: "constant-arrival-rate",
      rate: Number(__ENV.RATE || 100), // requests per second across VUs
      timeUnit: "1s",
      duration: "60s",
      preAllocatedVUs: 50,
      maxVUs: 200,
    },
  },
  thresholds: {
    // Target from ADR-0016: p95 < 100ms for balance reads.
    http_req_duration: ["p(95)<100"],
    checks: ["rate>0.99"],
  },
};

// login retries through the auth rate limiter (10 rps/IP) so every setup
// user reliably gets a token — otherwise tokenless VUs would pollute the
// measured read error rate with 401s (a test artifact, not a system fault).
function provision(email, name) {
  const h = { headers: { "Content-Type": "application/json" } };
  http.post(`${BASE}/v1/auth/register`, JSON.stringify({ email, password: "k6-password-1", name }), h);
  for (let attempt = 0; attempt < 10; attempt++) {
    const login = http.post(`${BASE}/v1/auth/login`, JSON.stringify({ email, password: "k6-password-1" }), h);
    if (login.status === 200) return login.json("access_token");
    sleep(0.3); // backoff past the fixed-window limiter
  }
  return null;
}

export function setup() {
  const runId = Date.now();
  const users = [];
  for (let i = 0; i < 20; i++) {
    const token = provision(`k6r-${runId}-${i}@load.kz`, `K6 Reader ${i}`);
    if (!token) continue;
    http.post(`${BASE}/v1/accounts`, JSON.stringify({ currency: "KZT" }), {
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    });
    users.push({ token });
    sleep(0.15);
  }
  return { users };
}

export default function (data) {
  const me = data.users[Math.floor(Math.random() * data.users.length)];
  const res = http.get(`${BASE}/v1/accounts`, {
    headers: { Authorization: `Bearer ${me.token}` },
  });
  // 429 is a correct answer from the limiter at high per-user rates.
  check(res, { "ok or limited": (r) => r.status === 200 || r.status === 429 });
  sleep(0.05);
}
