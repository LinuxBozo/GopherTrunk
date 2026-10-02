import { Section } from "../components/Section";
import { BoolField, Fieldset, NumberField, SelectField, TextField } from "../components/fields";
import { ListEditor } from "../components/ListEditor";
import { formatCommaList, parseCommaList } from "../lib/csvList";
import { useSection } from "./useSection";
import type { AlertChannelConfig, AlertRuleConfig, AlertsConfig } from "../api/types";

const CHANNEL_TYPES = [
  { value: "discord", label: "Discord webhook" },
  { value: "slack", label: "Slack webhook" },
  { value: "ntfy", label: "ntfy" },
  { value: "pushover", label: "Pushover" },
  { value: "telegram", label: "Telegram bot" },
  { value: "gotify", label: "Gotify" },
  { value: "webhook", label: "Generic JSON webhook" },
  { value: "exec", label: "Run a command" },
  { value: "mqtt", label: "MQTT broker" },
];

const EVENT_KINDS =
  "call.start, call.end, call.complete, grant, tone.alert, cc.locked, cc.lost, affiliation, registration, patch, call.encryption, talker.alias, location";

function formatNumList(v: number[] | null): string {
  return (v ?? []).join(", ");
}
function parseNumList(text: string): number[] | null {
  const out = text
    .split(/[,\s]+/)
    .map((t) => t.trim())
    .filter(Boolean)
    .map((t) => Number(t))
    .filter((n) => Number.isFinite(n) && n >= 0);
  return out.length ? out : null;
}

export function AlertsSection() {
  const [cfg, set] = useSection("Alerts");
  const c = (cfg as AlertsConfig) ?? ({ Channels: null, Rules: null } as AlertsConfig);
  const channelNames = (c.Channels ?? []).map((ch) => ch.Name).filter(Boolean);
  return (
    <Section sectionKey="alerts" title="Alerts">
      <Fieldset legend="Channels">
        <ListEditor<AlertChannelConfig>
          label="Channels"
          items={c.Channels}
          onChange={(x) => set({ ...c, Channels: x })}
          makeNew={() => ({
            Name: "",
            Type: "discord",
            URL: "",
            Token: "",
            User: "",
            Password: "",
            Priority: 0,
            Topic: "",
            MirrorEvents: false,
            Command: "",
            Timeout: "",
          })}
          itemTitle={(ch) => ch.Name || ch.Type || "channel"}
          emptyHint="No notification channels. Add one (Discord, ntfy, Pushover, Telegram, MQTT …) and reference it from a rule."
          renderItem={(ch, setCh) => {
            const t = ch.Type;
            const needsURL = ["discord", "slack", "ntfy", "gotify", "webhook", "mqtt"].includes(t);
            return (
              <div className="space-y-3">
                <div className="grid gap-3 sm:grid-cols-2">
                  <TextField label="Name" value={ch.Name} onChange={(v) => setCh({ ...ch, Name: v })} help="Rules reference this name." />
                  <SelectField label="Type" value={ch.Type} options={CHANNEL_TYPES} onChange={(v) => setCh({ ...ch, Type: v })} />
                  {needsURL && (
                    <TextField
                      label={t === "mqtt" ? "Broker URL" : "URL"}
                      value={ch.URL}
                      onChange={(v) => setCh({ ...ch, URL: v })}
                      help={
                        t === "mqtt"
                          ? "tcp://host:1883 or ssl://host:8883"
                          : t === "ntfy"
                            ? "Topic URL, e.g. https://ntfy.sh/my-scanner"
                            : t === "gotify"
                              ? "Gotify server base URL"
                              : "Incoming webhook / endpoint URL"
                      }
                    />
                  )}
                  {["pushover", "telegram", "gotify", "ntfy", "webhook"].includes(t) && (
                    <TextField
                      label="Token"
                      value={ch.Token}
                      onChange={(v) => setCh({ ...ch, Token: v })}
                      help={
                        t === "pushover"
                          ? "Application token"
                          : t === "telegram"
                            ? "Bot token"
                            : t === "gotify"
                              ? "Application token"
                              : t === "ntfy"
                                ? "Access token (optional)"
                                : "Bearer token (optional)"
                      }
                    />
                  )}
                  {["pushover", "telegram", "mqtt", "ntfy"].includes(t) && (
                    <TextField
                      label={t === "pushover" ? "User key" : t === "telegram" ? "Chat id" : "User"}
                      value={ch.User}
                      onChange={(v) => setCh({ ...ch, User: v })}
                    />
                  )}
                  {["mqtt", "ntfy"].includes(t) && (
                    <TextField label="Password" value={ch.Password} onChange={(v) => setCh({ ...ch, Password: v })} />
                  )}
                  {["ntfy", "pushover", "gotify"].includes(t) && (
                    <NumberField
                      label="Priority"
                      value={ch.Priority}
                      onChange={(v) => setCh({ ...ch, Priority: v })}
                      help={t === "ntfy" ? "1 (min) … 5 (urgent); 0 = default" : t === "pushover" ? "-2 … 2; 0 = normal" : "0 … 10; 0 = default"}
                    />
                  )}
                  {t === "mqtt" && (
                    <>
                      <TextField label="Topic prefix" value={ch.Topic} onChange={(v) => setCh({ ...ch, Topic: v })} help="Default gophertrunk → gophertrunk/alerts/<rule>" />
                      <BoolField label="Mirror every event" value={ch.MirrorEvents} onChange={(v) => setCh({ ...ch, MirrorEvents: v })} help="Also publish every bus event to <prefix>/events/<kind>." />
                    </>
                  )}
                  {t === "exec" && (
                    <TextField label="Command" value={ch.Command} onChange={(v) => setCh({ ...ch, Command: v })} help="Program + arguments (no shell). Alert JSON on stdin, GT_ALERT_* in the environment." />
                  )}
                  <TextField label="Timeout" value={ch.Timeout} onChange={(v) => setCh({ ...ch, Timeout: v })} help="Per delivery, e.g. 10s (default)." />
                </div>
              </div>
            );
          }}
        />
      </Fieldset>

      <Fieldset legend="Rules">
        <ListEditor<AlertRuleConfig>
          label="Rules"
          items={c.Rules}
          onChange={(x) => set({ ...c, Rules: x })}
          makeNew={() => ({
            Name: "",
            Disabled: false,
            On: ["call.start"],
            Systems: null,
            Talkgroups: null,
            Radios: null,
            Emergency: false,
            Encrypted: "",
            ToneProfiles: null,
            MinDurationMs: 0,
            Cooldown: "",
            Channels: channelNames.length ? [channelNames[0]] : null,
            Message: "",
            AttachAudio: false,
          })}
          itemTitle={(r) => r.Name || "rule"}
          emptyHint="No alert rules."
          renderItem={(r, setR) => (
            <div className="space-y-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <TextField label="Name" value={r.Name} onChange={(v) => setR({ ...r, Name: v })} />
                <BoolField label="Disabled" value={r.Disabled} onChange={(v) => setR({ ...r, Disabled: v })} />
                <TextField label="On events" value={formatCommaList(r.On)} onChange={(v) => setR({ ...r, On: parseCommaList(v) })} help={`Comma-separated: ${EVENT_KINDS}. Empty = call.start.`} />
                <TextField label="Channels" value={formatCommaList(r.Channels)} onChange={(v) => setR({ ...r, Channels: parseCommaList(v) })} help={channelNames.length ? `Available: ${channelNames.join(", ")}` : "Define a channel above first."} />
                <TextField label="Systems" value={formatCommaList(r.Systems)} onChange={(v) => setR({ ...r, Systems: parseCommaList(v) })} help="Comma-separated system names; empty = any." />
                <TextField label="Talkgroups" value={formatNumList(r.Talkgroups)} onChange={(v) => setR({ ...r, Talkgroups: parseNumList(v) })} help="Comma-separated talkgroup IDs; empty = any." />
                <TextField label="Radios" value={formatNumList(r.Radios)} onChange={(v) => setR({ ...r, Radios: parseNumList(v) })} help="Comma-separated source radio IDs; empty = any." />
                <TextField label="Tone profiles" value={formatCommaList(r.ToneProfiles)} onChange={(v) => setR({ ...r, ToneProfiles: parseCommaList(v) })} help="tone.alert only: tone-out profile names." />
                <BoolField label="Emergency only" value={r.Emergency} onChange={(v) => setR({ ...r, Emergency: v })} />
                <SelectField
                  label="Encrypted"
                  value={r.Encrypted}
                  options={[
                    { value: "", label: "any" },
                    { value: "only", label: "encrypted only" },
                    { value: "exclude", label: "clear only" },
                  ]}
                  onChange={(v) => setR({ ...r, Encrypted: v })}
                />
                <NumberField label="Min duration (ms)" value={r.MinDurationMs} onChange={(v) => setR({ ...r, MinDurationMs: v })} help="call.end / call.complete only." />
                <TextField label="Cooldown" value={r.Cooldown} onChange={(v) => setR({ ...r, Cooldown: v })} help="e.g. 30s, 5m — per system + talkgroup." />
                <BoolField label="Attach audio" value={r.AttachAudio} onChange={(v) => setR({ ...r, AttachAudio: v })} help="call.complete rules: attach the recording (Discord / Telegram / webhook)." />
              </div>
              <TextField label="Message template" value={r.Message} onChange={(v) => setR({ ...r, Message: v })} help="Optional Go template, e.g. {{.System}} TG {{.TalkgroupAlpha}} from {{.Source}}. Empty = built-in message." />
            </div>
          )}
        />
      </Fieldset>
    </Section>
  );
}
