import { Section } from "../components/Section";
import { BoolField, NumberField, SelectField, TextField } from "../components/fields";
import { formatCommaList, parseCommaList } from "../lib/csvList";
import { useSection } from "./useSection";
import type { TranscriptionConfig } from "../api/types";

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

export function TranscriptionSection() {
  const [cfg, set] = useSection("Transcription");
  const c =
    (cfg as TranscriptionConfig) ??
    ({
      Enabled: false,
      URL: "",
      APIKey: "",
      Model: "",
      Language: "",
      Prompt: "",
      Systems: null,
      Talkgroups: null,
      MinDurationMs: 0,
      Workers: 0,
      Timeout: "",
      UploadFormat: "",
      SkipEncrypted: null,
    } as TranscriptionConfig);
  return (
    <Section sectionKey="transcription" title="Transcription">
      <BoolField label="Enabled" value={c.Enabled} onChange={(v) => set({ ...c, Enabled: v })} help="Send each finished recording to the speech-to-text server and keep the text with the call." />
      <div className="grid gap-3 sm:grid-cols-2">
        <TextField label="URL" value={c.URL} onChange={(v) => set({ ...c, URL: v })} help="https://api.openai.com/v1/audio/transcriptions, or a local whisper.cpp server's http://host:8080/inference." />
        <TextField label="API key" value={c.APIKey} onChange={(v) => set({ ...c, APIKey: v })} help="Sent as Authorization: Bearer. Empty for a local server." />
        <TextField label="Model" value={c.Model} onChange={(v) => set({ ...c, Model: v })} placeholder="whisper-1" help="OpenAI model name; local servers ignore it." />
        <TextField label="Language" value={c.Language} onChange={(v) => set({ ...c, Language: v })} placeholder="en" help="ISO-639-1 hint; empty = auto-detect." />
        <TextField label="Systems" value={formatCommaList(c.Systems)} onChange={(v) => set({ ...c, Systems: parseCommaList(v) })} help="Comma-separated system names; empty = all." />
        <TextField label="Talkgroups" value={formatNumList(c.Talkgroups)} onChange={(v) => set({ ...c, Talkgroups: parseNumList(v) })} help="Comma-separated talkgroup IDs; empty = all." />
        <NumberField label="Min duration (ms)" value={c.MinDurationMs} onChange={(v) => set({ ...c, MinDurationMs: v })} help="Skip recordings shorter than this (default 1000)." />
        <NumberField label="Workers" value={c.Workers} onChange={(v) => set({ ...c, Workers: v })} help="Concurrent requests (default 1)." />
        <TextField label="Timeout" value={c.Timeout} onChange={(v) => set({ ...c, Timeout: v })} placeholder="60s" />
        <SelectField
          label="Upload format"
          value={c.UploadFormat}
          options={[
            { value: "", label: "16 kHz WAV (default)" },
            { value: "wav16k", label: "16 kHz WAV" },
            { value: "original", label: "original file" },
          ]}
          onChange={(v) => set({ ...c, UploadFormat: v })}
        />
        <BoolField label="Skip encrypted" value={c.SkipEncrypted ?? true} onChange={(v) => set({ ...c, SkipEncrypted: v })} help="Don't transcribe encrypted calls (default on)." />
      </div>
      <TextField label="Prompt" value={c.Prompt} onChange={(v) => set({ ...c, Prompt: v })} help="Vocabulary hint: unit names, street names, ten-codes." />
    </Section>
  );
}
