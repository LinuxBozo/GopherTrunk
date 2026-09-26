import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

vi.mock("../api/mdc1200", () => ({
  fetchMDC1200Messages: vi.fn(),
}));

import { fetchMDC1200Messages } from "../api/mdc1200";
import { useShared } from "../store/shared";
import { MDC1200 } from "./MDC1200";

function resetStore() {
  useShared.setState({
    serverURL: "http://localhost:8080",
    token: null,
    connected: true,
    wsStatus: "idle",
    mutations: null,
    lastError: null,
    events: [],
    activeCalls: [],
    devices: [],
    systems: [],
    talkgroups: [],
    health: null,
    audio: null,
    scanner: null,
  });
}

describe("MDC1200 panel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetStore();
  });

  it("renders an empty-state when no bursts are present", async () => {
    vi.mocked(fetchMDC1200Messages).mockResolvedValue([]);
    render(<MDC1200 />);
    await waitFor(() => {
      expect(screen.getByText(/No MDC1200 bursts yet/)).toBeInTheDocument();
    });
  });

  it("renders a PTT-ID burst with unit ID and operation", async () => {
    vi.mocked(fetchMDC1200Messages).mockResolvedValue([
      {
        id: 1,
        received_at: "2026-05-26T12:34:56Z",
        op: 0x01,
        arg: 0x80,
        unit_id: 0x1234,
        operation: "PTT ID",
        body: "Unit 1234: PTT ID",
        crc_ok: true,
        serial: "R2",
        frequency_hz: 146_670_000,
      },
    ]);
    render(<MDC1200 />);
    await waitFor(() => {
      expect(screen.getByText("1234")).toBeInTheDocument();
      expect(screen.getByText("PTT ID")).toBeInTheDocument();
      expect(screen.getByText("0x01 / 0x80")).toBeInTheDocument();
      // #1220: the channel the burst came in on — frequency and SDR serial.
      expect(screen.getByText(/146\.6700 MHz/)).toBeInTheDocument();
      expect(screen.getByText(/R2/)).toBeInTheDocument();
    });
  });

  it("renders an emergency burst", async () => {
    vi.mocked(fetchMDC1200Messages).mockResolvedValue([
      {
        id: 2,
        received_at: "2026-05-26T12:34:56Z",
        op: 0x00,
        arg: 0x90,
        unit_id: 0x0042,
        operation: "Emergency",
        body: "Unit 0042: Emergency",
        crc_ok: true,
      },
    ]);
    render(<MDC1200 />);
    await waitFor(() => {
      expect(screen.getByText("0042")).toBeInTheDocument();
      expect(screen.getByText("Emergency")).toBeInTheDocument();
    });
  });

  it("surfaces fetch errors", async () => {
    vi.mocked(fetchMDC1200Messages).mockRejectedValue(new Error("daemon down"));
    render(<MDC1200 />);
    await waitFor(() => {
      expect(screen.getByText(/daemon down/)).toBeInTheDocument();
    });
  });
});
