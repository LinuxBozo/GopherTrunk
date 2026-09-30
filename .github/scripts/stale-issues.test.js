// Policy tests for stale-issues.js: node --test .github/scripts/stale-issues.test.js
const test = require("node:test");
const assert = require("node:assert/strict");
const { decide, warningBody, closeBody } = require("./stale-issues.js");

const DAY = 24 * 60 * 60 * 1000;
const T0 = Date.parse("2026-09-01T12:00:00Z"); // the maintainer's last reply

function issue(extra = {}) {
  return {
    number: 1,
    state: "open",
    author_association: "NONE",
    user: { login: "reporter", type: "User" },
    labels: [],
    ...extra,
  };
}
const reporter = (atMs) => ({
  user: { login: "reporter", type: "User" },
  author_association: "NONE",
  created_at: new Date(atMs).toISOString(),
  body: "any update?",
});
const maintainer = (atMs) => ({
  user: { login: "MattCheramie", type: "User" },
  author_association: "OWNER",
  created_at: new Date(atMs).toISOString(),
  html_url: "https://example.invalid/c",
  body: "could you send a capture?",
});
const reminder = (stage, atMs) => ({
  user: { login: "github-actions[bot]", type: "Bot" },
  author_association: "NONE",
  created_at: new Date(atMs).toISOString(),
  body: `<!-- stale-issues:${stage} -->\nreminder`,
});

test("nothing before 7 days", () => {
  assert.equal(decide(issue(), [reporter(T0 - DAY), maintainer(T0)], T0 + 6.9 * DAY).action, "none");
});

test("each stage is posted once, in order", () => {
  const base = [reporter(T0 - DAY), maintainer(T0)];
  let d = decide(issue(), base, T0 + 7 * DAY);
  assert.deepEqual([d.action, d.stage], ["warn", 7]);
  // The reminder itself is not activity: the clock keeps running from T0.
  const after7 = [...base, reminder(7, T0 + 7 * DAY)];
  assert.equal(decide(issue(), after7, T0 + 14 * DAY).action, "none");
  d = decide(issue(), after7, T0 + 15 * DAY);
  assert.deepEqual([d.action, d.stage, d.days], ["warn", 15, 15]);
  assert.equal(decide(issue(), [...after7, reminder(15, T0 + 15 * DAY)], T0 + 16 * DAY).action, "none");
});

test("run daily, it posts only on days 7/15/30/45/55 and closes on 60", () => {
  // The workflow's real cadence: one run a day at 14:17 UTC, plus a second
  // (manual) run the same day to show a re-run never double-posts. The
  // maintainer's comment lands at 09:00, before the run, and at 20:00, after
  // it: whole days of inactivity decide, whatever the time of day.
  for (const hour of [9, 20]) {
    const last = Date.parse(`2026-10-01T${String(hour).padStart(2, "0")}:00:00Z`);
    const c = [maintainer(last)];
    const acted = [];
    for (let run = 0; run <= 75; run++) {
      for (const offset of [0, 3 * 60 * 60 * 1000]) {
        const now = Date.parse("2026-10-01T14:17:00Z") + run * DAY + offset;
        const d = decide(issue(), c, now);
        if (d.action === "warn") {
          c.push(reminder(d.stage, now));
          acted.push(`${d.days}:${d.stage}`);
        } else if (d.action === "close") {
          acted.push(`${d.days}:close`);
          break;
        }
      }
      if (acted.at(-1)?.endsWith(":close")) break;
    }
    assert.deepEqual(acted, ["7:7", "15:15", "30:30", "45:45", "55:55", "60:close"], `last reply at ${hour}:00`);
  }
});

test("a missed run jumps to the current stage, no burst", () => {
  const d = decide(issue(), [maintainer(T0), reminder(7, T0 + 7 * DAY)], T0 + 31 * DAY);
  assert.deepEqual([d.action, d.stage], ["warn", 30]);
});

test("stages 45 and 55 fire, then close at 60 after the final notice", () => {
  const c = [maintainer(T0), reminder(7, T0 + 7 * DAY), reminder(15, T0 + 15 * DAY), reminder(30, T0 + 30 * DAY)];
  assert.equal(decide(issue(), c, T0 + 45 * DAY).stage, 45);
  c.push(reminder(45, T0 + 45 * DAY));
  const d55 = decide(issue(), c, T0 + 55 * DAY);
  assert.equal(d55.stage, 55);
  assert.equal(d55.closeAtMs, T0 + 60 * DAY);
  c.push(reminder(55, T0 + 55 * DAY));
  assert.equal(decide(issue(), c, T0 + 59.9 * DAY).action, "none");
  assert.equal(decide(issue(), c, T0 + 60 * DAY).action, "close");
});

test("an issue first seen late gets a full final notice before closing", () => {
  const c = [maintainer(T0)];
  const d = decide(issue(), c, T0 + 70 * DAY);
  assert.deepEqual([d.action, d.stage], ["warn", 55]);
  assert.equal(d.closeAtMs, T0 + 75 * DAY);
  c.push(reminder(55, T0 + 70 * DAY));
  assert.equal(decide(issue(), c, T0 + 72 * DAY).action, "none");
  assert.equal(decide(issue(), c, T0 + 75 * DAY).action, "close");
});

test("a 55-day stage reached with <5 days to go still gives 5 days", () => {
  // Runs were missed: first seen at day 58 with only the 45-day reminder up.
  const d = decide(issue(), [maintainer(T0), reminder(45, T0 + 45 * DAY)], T0 + 58 * DAY);
  assert.deepEqual([d.action, d.stage], ["warn", 55]);
  assert.equal(d.closeAtMs, T0 + 63 * DAY);
});

test("any human reply resets the clock and clears the label", () => {
  const c = [maintainer(T0), reminder(7, T0 + 7 * DAY), reminder(15, T0 + 15 * DAY), reporter(T0 + 20 * DAY)];
  // Now waiting on the maintainer: left alone, label removed.
  const d = decide(issue({ labels: [{ name: "stale" }] }), c, T0 + 40 * DAY);
  assert.deepEqual([d.action, d.unlabel], ["skip", true]);
  // Maintainer answers: a fresh sequence from that answer.
  c.push(maintainer(T0 + 21 * DAY));
  const fresh = decide(issue({ labels: [{ name: "stale" }] }), c, T0 + 23 * DAY);
  assert.deepEqual([fresh.action, fresh.unlabel], ["none", true]);
  assert.equal(decide(issue(), c, T0 + 28 * DAY).stage, 7);
});

test("issues waiting on a maintainer are never nudged", () => {
  assert.equal(decide(issue(), [], T0 + 90 * DAY).action, "skip"); // no reply at all
  assert.equal(decide(issue(), [maintainer(T0), reporter(T0 + DAY)], T0 + 90 * DAY).action, "skip");
});

test("exemptions: maintainer-opened, keep-open/pinned/security, pull requests", () => {
  const c = [maintainer(T0)];
  assert.equal(decide(issue({ author_association: "OWNER" }), c, T0 + 90 * DAY).action, "skip");
  for (const l of ["keep-open", "pinned", "security"]) {
    assert.equal(decide(issue({ labels: [{ name: l }] }), c, T0 + 90 * DAY).action, "skip");
  }
  assert.equal(decide(issue({ pull_request: {} }), c, T0 + 90 * DAY).action, "skip");
});

test("a human quoting the marker is a human comment, not a reminder", () => {
  const spoof = { ...reporter(T0 + 2 * DAY), body: "<!-- stale-issues:55 --> quoted" };
  assert.equal(decide(issue(), [maintainer(T0), spoof], T0 + 70 * DAY).action, "skip");
});

test("comment bodies carry the marker, the date and the reporter", () => {
  const d = decide(issue(), [maintainer(T0)], T0 + 7 * DAY);
  const body = warningBody(issue(), d);
  assert.match(body, /^<!-- stale-issues:7 -->/);
  assert.match(body, /@reporter/);
  assert.match(body, /31 October 2026/);
  assert.match(body, /example\.invalid\/c/);
  assert.match(closeBody(issue(), { days: 60 }), /^<!-- stale-issues:close -->/);
});
