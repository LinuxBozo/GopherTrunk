import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

vi.mock("../api/acars", () => ({
  fetchACARSMessages: vi.fn(),
}));

import { fetchACARSMessages } from "../api/acars";
import { useShared } from "../store/shared";
import { ACARS } from "./ACARS";

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

describe("ACARS panel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetStore();
  });

  it("renders an empty-state when no messages are present", async () => {
    vi.mocked(fetchACARSMessages).mockResolvedValue([]);
    render(<ACARS />);
    await waitFor(() => {
      expect(screen.getByText(/No ACARS messages yet/)).toBeInTheDocument();
    });
  });

  it("renders a downlink with registration, flight, label and text", async () => {
    vi.mocked(fetchACARSMessages).mockResolvedValue([
      {
        id: 1,
        received_at: "2026-10-01T10:56:28Z",
        mode: "2",
        address: "G-DBCK",
        ack: "!",
        label: "H1",
        block_id: "3",
        downlink: true,
        msg_no: "D65C",
        flight_id: "BA031T",
        text: "#DFB/POS N51 W001",
        crc_ok: true,
        serial: "00000001",
        frequency_hz: 131_125_000,
      },
    ]);
    render(<ACARS />);
    await waitFor(() => {
      expect(screen.getByText("G-DBCK")).toBeInTheDocument();
      expect(screen.getByText("BA031T")).toBeInTheDocument();
      expect(screen.getByText("down")).toBeInTheDocument();
      expect(screen.getByText(/#DFB\/POS N51 W001/)).toBeInTheDocument();
      expect(screen.getByText(/131\.1250 MHz/)).toBeInTheDocument();
      expect(screen.getByText("ok")).toBeInTheDocument();
    });
  });

  it("marks a repaired block and a failed one", async () => {
    vi.mocked(fetchACARSMessages).mockResolvedValue([
      {
        id: 2,
        received_at: "2026-10-01T10:56:28Z",
        address: "N123GT",
        label: "Q0",
        downlink: false,
        crc_ok: true,
        corrected: 1,
      },
      {
        id: 3,
        received_at: "2026-10-01T10:56:29Z",
        address: "N124GT",
        label: "Q0",
        downlink: false,
        crc_ok: false,
      },
    ]);
    render(<ACARS />);
    await waitFor(() => {
      expect(screen.getByText("fixed 1")).toBeInTheDocument();
      expect(screen.getByText("fail")).toBeInTheDocument();
      expect(screen.getAllByText("up")).toHaveLength(2);
    });
  });

  it("surfaces fetch errors", async () => {
    vi.mocked(fetchACARSMessages).mockRejectedValue(new Error("daemon down"));
    render(<ACARS />);
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/daemon down/);
    });
  });
});
