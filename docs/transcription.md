---
layout: page
title: Transcription
description: Speech-to-text for finished recordings via any OpenAI-compatible Whisper server — transcripts on the call, searchable, exportable, and usable as alert keywords
nav_group: Reference
---

# Transcription

`transcription:` sends every finished recording to a speech-to-text server
and keeps the text with the call. It is the transcripts column the
call-playback ecosystem (Scanner Radio's live transcriptions,
trunk-recorder's transcription plugins) has grown, with a keyword hook into
[alerts](alerts.html).

GopherTrunk runs **no model itself**. It speaks the OpenAI
audio-transcriptions request shape — multipart `file` + `model` +
`language` (+ optional `prompt`), JSON `{"text": …}` back — which is what
these all expose:

| Server | `url` |
|---|---|
| OpenAI | `https://api.openai.com/v1/audio/transcriptions` + `api_key` |
| [whisper.cpp](https://github.com/ggerganov/whisper.cpp) `server` | `http://host:8080/inference` |
| faster-whisper-server / Speaches | `http://host:8000/v1/audio/transcriptions` |
| LocalAI | `http://host:8080/v1/audio/transcriptions` |

```yaml
transcription:
  enabled: true
  url: "http://127.0.0.1:8080/inference"
  language: "en"
  prompt: "Engine 1, Medic 7, Main Street"
  systems: ["Metro P25"]
  min_duration_ms: 1000
```

## What happens to the text

- **Stored on the call** (`call_log.transcript`): the History panel shows a
  Transcript column and the full text in the call detail; the `q` search
  matches transcript text; `GET /api/v1/calls/history?format=csv` exports
  it. A multi-over call's segments append in order.
- **Published** as the `call.transcript` event (`system`, `group_id`,
  `source_id`, `call_started_at`, `segment`, `audio_path`, `text`).
- **Alertable**: an `alerts:` rule with `on: [call.transcript]` and
  `keywords: ["shots fired", "structure fire"]` fires only when the text
  contains one of the words — with `attach_audio: true` the recording rides
  along to Discord / Telegram.

## Audio sent

By default the recording (WAV or FLAC) is converted to **16 kHz mono 16-bit
WAV** (`upload_format: wav16k`), the one input every Whisper server
accepts — whisper.cpp refuses anything else. `original` uploads the file as
recorded. Encrypted calls are skipped (`skip_encrypted`, default on) and so
are recordings shorter than `min_duration_ms` (default 1 s).

## Operating it

`GET /api/v1/transcription` reports sent / failed / skipped / dropped
counts, the mean request latency, the audio seconds transcribed and the
last transcript. Requests run on `workers` goroutines (default 1 — a local
server serialises anyway and a hosted one rate-limits) behind a bounded
queue; a dead server never stalls the recorder. `timeout` (default 60 s)
bounds one request.

A local `whisper.cpp` server on a small box keeps up with a busy system
with the `base.en` or `small.en` model; `prompt` with your unit names and
street names measurably improves proper nouns.
