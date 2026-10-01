import { useMemo, useState } from "react";
import { fetchACARSMessages, type ACARSMessage } from "../api/acars";
import { Column, DataTable } from "../components/DataTable";
import { PageHeader } from "../components/ui/PageHeader";
import { Badge } from "../components/ui/Badge";
import { StaleIndicator } from "../components/ui/StaleIndicator";
import { useDataPoll } from "../hooks/useDataPoll";
import { selectClientConfig, useShared } from "../store/shared";
import { formatClock } from "../lib/formatTime";

// ACARS panel (#1231) — recent decoded VHF air-band ACARS blocks from
// scanner.conventional AM channels with `decoders: [acars]`. Each row shows
// the aircraft registration, flight and message number (downlinks), label,
// block id, the text, the channel that produced it, and whether the block
// check validated (and how many bits were repaired first).
//
// Polls /api/v1/acars/messages every 5 s.

const POLL_INTERVAL_MS = 5_000;

export function ACARS() {
  const cfg = useShared(selectClientConfig);
  const [messages, setMessages] = useState<ACARSMessage[]>([]);

  const { loading, error, stale, lastUpdated } = useDataPoll({
    fetcher: () => fetchACARSMessages(cfg, 200),
    onData: setMessages,
    intervalMs: POLL_INTERVAL_MS,
    resetKey: cfg.baseURL,
  });

  const columns: Column<ACARSMessage>[] = useMemo(
    () => [
      {
        key: "received",
        header: "Received",
        render: (m) => (
          <span className="font-mono text-muted">{formatClock(m.received_at)}</span>
        ),
        sort: (a, b) => a.received_at.localeCompare(b.received_at),
      },
      {
        key: "address",
        header: "Aircraft",
        render: (m) => <span className="font-mono text-accent">{m.address || "—"}</span>,
        sort: (a, b) => a.address.localeCompare(b.address),
      },
      {
        key: "flight",
        header: "Flight",
        render: (m) => <span className="font-mono">{m.flight_id?.trim() || "—"}</span>,
        sort: (a, b) => (a.flight_id ?? "").localeCompare(b.flight_id ?? ""),
      },
      {
        key: "dir",
        header: "Dir",
        render: (m) => (
          <Badge tone="neutral" title={m.downlink ? "air → ground" : "ground → air"}>
            {m.downlink ? "down" : "up"}
          </Badge>
        ),
        sort: (a, b) => Number(a.downlink) - Number(b.downlink),
      },
      {
        key: "label",
        header: "Label",
        render: (m) => (
          <span className="font-mono" title={m.block_id ? `block ${m.block_id}` : undefined}>
            {m.label}
            {m.msg_no ? <span className="text-muted"> · {m.msg_no}</span> : null}
          </span>
        ),
        sort: (a, b) => a.label.localeCompare(b.label),
      },
      {
        key: "text",
        header: "Text",
        render: (m) =>
          m.text ? (
            <span className="whitespace-pre-wrap break-all font-mono text-xs">
              {m.text}
              {m.more ? <span className="text-muted"> …</span> : null}
            </span>
          ) : (
            <span className="text-muted">—</span>
          ),
      },
      {
        key: "channel",
        header: "Channel",
        render: (m) =>
          m.frequency_hz ? (
            <span className="font-mono" title={m.serial ? `SDR ${m.serial}` : undefined}>
              {(m.frequency_hz / 1e6).toFixed(4)} MHz
            </span>
          ) : (
            <span className="text-muted">—</span>
          ),
        sort: (a, b) => (a.frequency_hz ?? 0) - (b.frequency_hz ?? 0),
      },
      {
        key: "crc",
        header: "Check",
        className: "text-right",
        headerClassName: "text-right",
        render: (m) =>
          !m.crc_ok ? (
            <Badge tone="err">fail</Badge>
          ) : m.corrected ? (
            <Badge tone="warn" title={`${m.corrected} bit(s) repaired before the block check validated`}>
              fixed {m.corrected}
            </Badge>
          ) : (
            <Badge tone="ok">ok</Badge>
          ),
      },
    ],
    [],
  );

  return (
    <div className="space-y-3">
      <PageHeader
        title="ACARS"
        actions={
          <>
            <StaleIndicator stale={stale} lastUpdated={lastUpdated} />
            <span className="text-xs text-muted">
              {messages.length} message{messages.length === 1 ? "" : "s"}
            </span>
          </>
        }
      />

      {error && !stale && (
        <div
          role="alert"
          className="rounded-md border border-err/40 bg-err/15 px-3 py-2 text-sm text-err"
        >
          {error}
        </div>
      )}

      <DataTable
        rows={messages}
        columns={columns}
        rowKey={(m) => String(m.id)}
        defaultSortKey="received"
        defaultSortDirection="desc"
        tableId="acars"
        pageSize={50}
        loading={loading}
        searchable
        searchAccessor={(m) =>
          [m.address, m.flight_id, m.label, m.msg_no, m.text].filter(Boolean).join(" ")
        }
        searchPlaceholder="Search by registration, flight, label, text…"
        emptyMessage={
          <>
            No ACARS messages yet. Add an AM channel to{" "}
            <code className="text-accent">scanner.conventional</code> on an ACARS
            frequency (131.550, 131.525, 130.025 MHz …) with{" "}
            <code className="text-accent">mode: am</code> and{" "}
            <code className="text-accent">decoders: [acars]</code>, and decoded
            blocks will land here.
          </>
        }
      />
    </div>
  );
}
