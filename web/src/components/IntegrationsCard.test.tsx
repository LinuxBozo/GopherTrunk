import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

vi.mock("../api/client", () => ({
  api: {
    alertsStatus: vi.fn(),
    transcriptionStatus: vi.fn(),
  },
}));
vi.mock("../api/write", () => ({
  writes: { testAlertChannel: vi.fn().mockResolvedValue({ ok: true, channel: "ops" }) },
}));

import { api } from "../api/client";
import { writes } from "../api/write";
import { useShared } from "../store/shared";
import { IntegrationsCard } from "./IntegrationsCard";

describe("IntegrationsCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useShared.setState({ serverURL: "http://localhost:8080", token: null });
  });

  it("says so when neither subsystem is configured", async () => {
    vi.mocked(api.alertsStatus).mockResolvedValue({
      configured: false, channels: [], rules: [], recent: [],
    });
    vi.mocked(api.transcriptionStatus).mockResolvedValue({ configured: false });
    render(<IntegrationsCard />);
    await waitFor(() => expect(screen.getAllByText("not configured")).toHaveLength(2));
    expect(screen.getByText(/alerts:/)).toBeInTheDocument();
  });

  it("lists alert channels with counters and fires the test endpoint", async () => {
    vi.mocked(api.alertsStatus).mockResolvedValue({
      configured: true,
      channels: [{ name: "ops", type: "discord", sent: 3, failed: 1, last_error: "503" }],
      rules: [{ name: "fire-tgs", channels: ["ops"], fired: 3, cooldown_suppressed: 0 }],
      matched: 4, queued: 0, dropped: 0,
      recent: [{ rule: "fire-tgs", at: "2026-10-02T12:00:00Z", title: "Call on 1234", text: "x", channels: ["ops"] }],
    });
    vi.mocked(api.transcriptionStatus).mockResolvedValue({
      configured: true, model: "whisper-1", sent: 10, failed: 0, skipped: 2,
      last_text: "engine 12 responding", last_at: "2026-10-02T12:00:00Z",
    });
    render(<IntegrationsCard />);
    await waitFor(() => expect(screen.getByText("ops")).toBeInTheDocument());
    expect(screen.getByText("3 sent / 1 failed")).toBeInTheDocument();
    expect(screen.getByText("1 rule")).toBeInTheDocument();
    expect(screen.getByText("whisper-1")).toBeInTheDocument();
    expect(screen.getByText(/engine 12 responding/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    await waitFor(() => expect(writes.testAlertChannel).toHaveBeenCalledTimes(1));
    expect(vi.mocked(writes.testAlertChannel).mock.calls[0][1]).toBe("ops");
  });
});
