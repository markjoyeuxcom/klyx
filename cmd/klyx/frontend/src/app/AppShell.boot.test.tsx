import { describe, it, expect, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";

// Health 1c slice 5 §1: every cluster's summary is read from app start - the
// fleet board, the fleet indicator and Settings never wait for a lens or a cluster.
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../chrome/NavBand", () => ({ NavBand: () => null }));
vi.mock("../chrome/ShortcutsOverlay", () => ({ ShortcutsOverlay: () => null }));
vi.mock("../chrome/LocationBar", () => ({ LocationBar: () => null }));
vi.mock("../fleet/FleetView", () => ({ FleetView: () => null }));
vi.mock("../fleet/FleetPromotion", () => ({ FleetPromotion: () => null }));
vi.mock("../fleet/FleetExpiry", () => ({ FleetExpiry: () => null }));
vi.mock("../fleet/ForwardsView", () => ({ ForwardsView: () => null }));
vi.mock("../fleet/ActivityView", () => ({ ActivityView: () => null }));
vi.mock("./SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../cluster/ClusterDetail", () => ({ ClusterDetail: () => null }));
vi.mock("../chrome/ActionToast", () => ({ ActionToast: () => null }));
vi.mock("../chrome/CommandPalette", () => ({ CommandPalette: () => null, getPaletteOpen: () => false }));
import { startSummaries } from "../bridge/healthRows";
import { useFleet } from "../store/fleet";
import { AppShell } from "./AppShell";

beforeEach(() => vi.clearAllMocks());

describe("summaries start from the app root", () => {
  it("on the fleet board and on Settings, before any cluster is opened", () => {
    useFleet.setState({ route: { name: "fleet" } });
    const r1 = render(<AppShell />);
    expect(startSummaries).toHaveBeenCalledTimes(1);
    r1.unmount();
    useFleet.setState({ route: { name: "settings" } });
    render(<AppShell />);
    expect(startSummaries).toHaveBeenCalledTimes(2); // the call is idempotent in the bridge
  });
});
