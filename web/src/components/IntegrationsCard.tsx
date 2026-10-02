import { useState } from "react";
import { api } from "../api/client";
import { writes } from "../api/write";
import type { AlertsStatusDTO, TranscriptionStatusDTO } from "../api/types";
import { Badge } from "./ui/Badge";
import { useDataPoll } from "../hooks/useDataPoll";
import { selectClientConfig, useShared } from "../store/shared";
import { formatClock } from "../lib/formatTime";

// IntegrationsCard surfaces the two optional outbound subsystems — alert
// notifications (GET /api/v1/alerts) and speech-to-text (GET
// /api/v1/transcription) — on the operations landing, with a per-channel
// "Test" button so an operator can confirm a webhook / ntfy topic / MQTT
// broker is reachable without waiting for a matching call. Both endpoints
// answer `configured: false` when the daemon has no such config section;
// the card then says so instead of hiding, so a missing section reads as
// "not set up" rather than "broken".
export function IntegrationsCard() {
  const cfg = useShared(selectClientConfig);
  const notify = useShared((s) => s.notify);
  const [alerts, setAlerts] = useState<AlertsStatusDTO | null>(null);
  const [transcription, setTranscription] = useState<TranscriptionStatusDTO | null>(null);
  const [testing, setTesting] = useState<string | null>(null);

  useDataPoll({
    fetcher: async () => {
      const [a, t] = await Promise.allSettled([
        api.alertsStatus(cfg),
        api.transcriptionStatus(cfg),
      ]);
      return {
        alerts: a.status === "fulfilled" ? a.value : null,
        transcription: t.status === "fulfilled" ? t.value : null,
      };
    },
    onData: (d) => {
      if (d.alerts) setAlerts(d.alerts);
      if (d.transcription) setTranscription(d.transcription);
    },
    intervalMs: 10_000,
    resetKey: cfg.baseURL,
  });

  const testChannel = async (name: string) => {
    setTesting(name);
    try {
      await writes.testAlertChannel(cfg, name);
      notify("success", `Test notification delivered via ${name}`);
    } catch (e) {
      notify("error", `Test via ${name} failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setTesting(null);
    }
  };

  return (
    <section className="panel p-4" data-testid="integrations-card">
      <div className="flex items-baseline justify-between mb-2">
        <h3 className="panel-title">Alerts &amp; transcription</h3>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
        <div>
          <p className="font-semibold mb-1">
            Alerts{" "}
            {alerts === null ? (
              <Badge>loading</Badge>
            ) : alerts.configured ? (
              <Badge tone="ok">{alerts.rules.length} rule{alerts.rules.length === 1 ? "" : "s"}</Badge>
            ) : (
              <Badge>not configured</Badge>
            )}
          </p>
          {alerts?.configured ? (
            <>
              <p className="text-muted">
                {alerts.matched ?? 0} matched · {alerts.queued ?? 0} queued
                {alerts.dropped ? ` · ${alerts.dropped} dropped` : ""}
              </p>
              <ul className="mt-1 space-y-1">
                {alerts.channels.map((ch) => (
                  <li key={ch.name} className="flex items-center gap-2">
                    <span className="font-mono">{ch.name}</span>
                    <span className="text-muted">{ch.type}</span>
                    <Badge tone={ch.failed > 0 && ch.sent === 0 ? "err" : ch.failed > 0 ? "warn" : "ok"}
                      title={ch.last_error}>
                      {ch.sent} sent{ch.failed ? ` / ${ch.failed} failed` : ""}
                    </Badge>
                    <button
                      type="button"
                      className="btn-ghost text-xs"
                      disabled={testing !== null}
                      onClick={() => void testChannel(ch.name)}
                    >
                      {testing === ch.name ? "Testing…" : "Test"}
                    </button>
                  </li>
                ))}
              </ul>
              {alerts.recent.length > 0 && (
                <p className="text-muted mt-1 truncate" title={alerts.recent[0].text}>
                  Last: {formatClock(alerts.recent[0].at)} {alerts.recent[0].rule} — {alerts.recent[0].title}
                </p>
              )}
            </>
          ) : alerts !== null ? (
            <p className="text-muted">
              Add an <code>alerts:</code> section to notify Discord, Slack, ntfy, Pushover,
              Telegram, Gotify, a webhook, a command or MQTT on matching calls.
            </p>
          ) : null}
        </div>
        <div>
          <p className="font-semibold mb-1">
            Transcription{" "}
            {transcription === null ? (
              <Badge>loading</Badge>
            ) : transcription.configured ? (
              <Badge tone={transcription.last_error && !transcription.sent ? "err" : "ok"}>
                {transcription.model || "whisper"}
              </Badge>
            ) : (
              <Badge>not configured</Badge>
            )}
          </p>
          {transcription?.configured ? (
            <>
              <p className="text-muted">
                {transcription.sent ?? 0} transcribed · {transcription.failed ?? 0} failed ·{" "}
                {transcription.skipped ?? 0} skipped
                {transcription.mean_latency_ms ? ` · ${transcription.mean_latency_ms} ms mean` : ""}
              </p>
              {transcription.last_error && (
                <p className="text-err truncate" title={transcription.last_error}>
                  {transcription.last_error}
                </p>
              )}
              {transcription.last_text && (
                <p className="text-muted mt-1 truncate" title={transcription.last_text}>
                  Last: {transcription.last_at ? `${formatClock(transcription.last_at)} ` : ""}
                  “{transcription.last_text}”
                </p>
              )}
            </>
          ) : transcription !== null ? (
            <p className="text-muted">
              Add a <code>transcription:</code> section pointing at any Whisper-compatible
              server to attach searchable text to recordings.
            </p>
          ) : null}
        </div>
      </div>
    </section>
  );
}
