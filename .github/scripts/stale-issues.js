// Staged stale-issue reminders for .github/workflows/stale-issues.yml.
//
// An issue is a candidate only while it is WAITING ON ITS REPORTER: the last
// human comment is from a maintainer (OWNER / MEMBER / COLLABORATOR). An issue
// whose last human word is the reporter's is waiting on us, and nudging the
// reporter about our own silence would be rude, so it is left alone.
//
// Inactivity is measured from the last HUMAN comment (or the issue's creation).
// This workflow's own reminders are not activity: they carry a hidden marker
// and are ignored when finding the last human comment, so the clock is not
// reset by the reminders themselves. Any human comment (reporter, maintainer,
// or anyone else) resets the clock, and the whole reminder sequence starts
// over.
//
// Reminders go out at 7, 15, 30, 45 and 55 days. At 60 days the issue is
// closed as "not planned", but only if the final (55-day) reminder has been up
// for at least FINAL_NOTICE_DAYS. An issue first seen late (for example an old
// one when this workflow is switched on) gets the final notice first, never a
// surprise close.

const STAGES = [7, 15, 30, 45, 55];
const CLOSE_DAYS = 60;
const FINAL_NOTICE_DAYS = 5;
const DAY_MS = 24 * 60 * 60 * 1000;

const STALE_LABEL = "stale";
// Any of these labels exempts an issue entirely.
const EXEMPT_LABELS = ["keep-open", "pinned", "security"];
const MAINTAINER_ASSOCIATIONS = ["OWNER", "MEMBER", "COLLABORATOR"];
const BOT_LOGIN = "github-actions[bot]";

const MARKER_RE = /<!-- stale-issues:(\d+|close) -->/;

function marker(stage) {
  return `<!-- stale-issues:${stage} -->`;
}

function isMaintainer(association) {
  return MAINTAINER_ASSOCIATIONS.includes(association);
}

// stageOf returns the reminder stage a comment carries ("close" or a number),
// or null when it is not one of this workflow's reminders. Only the workflow's
// own account can post a reminder; a human quoting a marker is still a human
// comment.
function stageOf(comment) {
  if (!comment.user || comment.user.login !== BOT_LOGIN) return null;
  const m = MARKER_RE.exec(comment.body || "");
  if (!m) return null;
  return m[1] === "close" ? "close" : Number(m[1]);
}

function formatDate(ms) {
  return new Date(ms).toLocaleDateString("en-GB", {
    day: "numeric",
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  });
}

// decide is the pure policy: given an issue, its comments (oldest first) and
// the current time, it returns what to do. It performs no I/O.
//
//   { action: "skip", reason, unlabel }
//   { action: "warn", stage, closeAtMs, days, lastMaintainerUrl }
//   { action: "close", days }
//   { action: "none", days, unlabel }   eligible, but nothing due today
function decide(issue, comments, nowMs) {
  const labels = (issue.labels || []).map((l) => (typeof l === "string" ? l : l.name));
  const hasStale = labels.includes(STALE_LABEL);

  if (issue.pull_request) return { action: "skip", reason: "pull request", unlabel: false };
  if (issue.state !== "open") return { action: "skip", reason: "not open", unlabel: false };
  const exempt = labels.find((l) => EXEMPT_LABELS.includes(l));
  if (exempt) return { action: "skip", reason: `exempt label "${exempt}"`, unlabel: hasStale };
  if (isMaintainer(issue.author_association)) {
    // Opened by a maintainer: a tracking issue, not a report awaiting someone.
    return { action: "skip", reason: "opened by a maintainer", unlabel: hasStale };
  }

  // Human comments are everything except bot accounts (this workflow's
  // reminders included).
  const human = comments.filter((c) => !(c.user && c.user.type === "Bot"));
  const lastHuman = human.length ? human[human.length - 1] : null;
  if (!lastHuman) {
    return { action: "skip", reason: "no reply yet: waiting on a maintainer", unlabel: hasStale };
  }
  if (!isMaintainer(lastHuman.author_association)) {
    return { action: "skip", reason: "last reply is not a maintainer's: waiting on a maintainer", unlabel: hasStale };
  }

  const lastHumanMs = Date.parse(lastHuman.created_at);
  const days = Math.floor((nowMs - lastHumanMs) / DAY_MS);

  // Reminders already posted since the last human comment.
  const posted = new Map(); // stage -> created_at ms
  for (const c of comments) {
    const s = stageOf(c);
    const at = Date.parse(c.created_at);
    if (s !== null && at > lastHumanMs) posted.set(s, at);
  }
  const final = STAGES[STAGES.length - 1];

  if (posted.size === 0 && days < STAGES[0]) {
    // Activity resumed (or the clock is young): clear a leftover label.
    return { action: "none", days, unlabel: hasStale };
  }

  if (days >= CLOSE_DAYS) {
    const finalAt = posted.get(final);
    if (finalAt !== undefined && nowMs - finalAt >= FINAL_NOTICE_DAYS * DAY_MS) {
      return { action: "close", days };
    }
    if (finalAt === undefined) {
      // Seen late: give the full final notice before any close.
      return {
        action: "warn",
        stage: final,
        closeAtMs: nowMs + FINAL_NOTICE_DAYS * DAY_MS,
        days,
        lastMaintainerUrl: lastHuman.html_url,
      };
    }
    return { action: "none", days, unlabel: false }; // final notice still running
  }

  // The highest stage reached, posted once. A run that was missed (or an issue
  // first seen late) jumps straight to the current stage instead of posting a
  // burst of catch-up reminders.
  const due = STAGES.filter((s) => s <= days).pop();
  const highestPosted = Math.max(-1, ...[...posted.keys()].filter((s) => s !== "close"));
  if (due !== undefined && due > highestPosted) {
    const closeAtMs =
      due === final
        ? Math.max(lastHumanMs + CLOSE_DAYS * DAY_MS, nowMs + FINAL_NOTICE_DAYS * DAY_MS)
        : lastHumanMs + CLOSE_DAYS * DAY_MS;
    return { action: "warn", stage: due, closeAtMs, days, lastMaintainerUrl: lastHuman.html_url };
  }
  return { action: "none", days, unlabel: false };
}

function warningBody(issue, d) {
  const who = issue.user && issue.user.login ? `@${issue.user.login}` : "there";
  const final = d.stage === STAGES[STAGES.length - 1];
  const opener = final
    ? `Hi ${who}, this is a final reminder. There's been no activity on this issue for ${d.days} days.`
    : `Hi ${who}, just checking in on this one. There's been no activity for ${d.days} days.`;
  return [
    marker(d.stage),
    `${opener} It's waiting on a reply to the [last update](${d.lastMaintainerUrl}), which asked for more information or a retest.`,
    "",
    `**If this is still relevant for you**, leave a comment (even just "still relevant") and it'll stay open. If there's no response by **${formatDate(d.closeAtMs)}**, it'll be closed as *not planned* due to inactivity.`,
    "",
    "Closing wouldn't mean the report wasn't valuable, or that the problem is fixed. It just keeps the tracker focused on what can move forward. You're always welcome to comment here or open a new issue that links back to this one, and we'll pick it up from there.",
    "",
    "Thanks for reporting this and for your help so far!",
    "",
    "<sub>This is an automated reminder. Reminders go out after 7, 15, 30, 45 and 55 days without a reply, and the issue closes after 60. Any comment resets the clock.</sub>",
  ].join("\n");
}

function closeBody(issue, d) {
  const who = issue.user && issue.user.login ? `@${issue.user.login}` : "there";
  return [
    marker("close"),
    `Hi ${who}, closing this as *not planned* after ${d.days} days without a reply, as the earlier reminders mentioned.`,
    "",
    "This doesn't mean the report wasn't valuable, or that the problem is fixed. If it's still happening, comment here and a maintainer can reopen it, or open a new issue that links back to this one. We'll pick it up from there.",
    "",
    "Thanks again for reporting it!",
  ].join("\n");
}

// run applies decide() to every open issue. dryRun logs without writing.
async function run({ github, context, core, dryRun }) {
  const { owner, repo } = context.repo;
  const nowMs = Date.now();
  const issues = await github.paginate(github.rest.issues.listForRepo, {
    owner,
    repo,
    state: "open",
    per_page: 100,
  });
  const summary = [];
  for (const issue of issues) {
    if (issue.pull_request) continue;
    const comments = await github.paginate(github.rest.issues.listComments, {
      owner,
      repo,
      issue_number: issue.number,
      per_page: 100,
    });
    const d = decide(issue, comments, nowMs);
    const n = issue.number;
    const ref = { owner, repo, issue_number: n };
    switch (d.action) {
      case "warn":
        core.info(`#${n}: ${d.days} days, posting the ${d.stage}-day reminder`);
        summary.push([`#${n}`, `${d.days}`, `reminder (${d.stage}-day)`]);
        if (!dryRun) {
          await github.rest.issues.createComment({ ...ref, body: warningBody(issue, d) });
          await github.rest.issues.addLabels({ ...ref, labels: [STALE_LABEL] });
        }
        break;
      case "close":
        core.info(`#${n}: ${d.days} days, closing as not planned`);
        summary.push([`#${n}`, `${d.days}`, "closed"]);
        if (!dryRun) {
          await github.rest.issues.createComment({ ...ref, body: closeBody(issue, d) });
          await github.rest.issues.update({ ...ref, state: "closed", state_reason: "not_planned" });
        }
        break;
      default:
        core.info(`#${n}: ${d.action}${d.reason ? ` (${d.reason})` : ""}${d.days !== undefined ? `, ${d.days} days` : ""}`);
        if (d.unlabel) {
          summary.push([`#${n}`, `${d.days ?? "-"}`, "stale label removed"]);
          if (!dryRun) {
            try {
              await github.rest.issues.removeLabel({ ...ref, name: STALE_LABEL });
            } catch (e) {
              if (e.status !== 404) throw e;
            }
          }
        }
    }
  }
  await core.summary
    .addHeading(dryRun ? "Stale issues (dry run: nothing posted)" : "Stale issues")
    .addTable([
      [
        { data: "Issue", header: true },
        { data: "Days inactive", header: true },
        { data: "Action", header: true },
      ],
      ...summary,
    ])
    .addRaw(summary.length ? "" : "Nothing due today.")
    .write();
}

module.exports = { decide, run, warningBody, closeBody, STAGES, CLOSE_DAYS, FINAL_NOTICE_DAYS };
