// ACARS (VHF air-band data link, #1231) client. Mirrors GET /api/v1/acars/messages.

import { type ClientConfig, joinURL } from "./client";

export interface ACARSMessage {
  id: number;
  received_at: string;
  mode?: string;
  /** Aircraft registration. */
  address: string;
  /** Technical acknowledgement; "!" is a NAK. */
  ack?: string;
  label: string;
  block_id?: string;
  /** Air-to-ground (block id is a digit). */
  downlink: boolean;
  msg_no?: string;
  flight_id?: string;
  text?: string;
  /** An ETB suffix: further blocks of this message follow. */
  more?: boolean;
  crc_ok: boolean;
  /** Bits repaired by parity / block-check correction before it validated. */
  corrected?: number;
  raw_hex?: string;
  /** SDR serial of the scanner that decoded the block. */
  serial?: string;
  /** The scan-list channel's frequency in Hz. */
  frequency_hz?: number;
}

export async function fetchACARSMessages(
  cfg: ClientConfig,
  limit = 200,
): Promise<ACARSMessage[]> {
  const url = joinURL(
    cfg.baseURL,
    `/api/v1/acars/messages?limit=${encodeURIComponent(String(limit))}`,
  );
  const headers: Record<string, string> = { Accept: "application/json" };
  if (cfg.token) headers["Authorization"] = `Bearer ${cfg.token}`;
  const res = await fetch(url, { headers });
  if (!res.ok) throw new Error(`acars/messages ${res.status}`);
  return (await res.json()) as ACARSMessage[];
}
