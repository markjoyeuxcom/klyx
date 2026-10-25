import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import type { GitRepo, GitSettings } from "../bridge/gitSettings";

const bridge = vi.hoisted(() => ({
  getGitSettings: vi.fn(),
  approveGitRepo: vi.fn(),
  removeGitRepo: vi.fn(),
  setGitRepoMatch: vi.fn(),
  fetchGitRepo: vi.fn(),
  clearGitCache: vi.fn(),
}));
vi.mock("../bridge/gitSettings", async (orig) => ({ ...(await orig<typeof import("../bridge/gitSettings")>()), ...bridge }));
vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn() }));

import { GitSettingsPanel, historyLine } from "./GitSettingsPanel";

function repo(over: Partial<GitRepo> = {}): GitRepo {
  return {
    url: "git@github.com:acme/fleet.git",
    key: "github.com/acme/fleet",
    matches: [],
    sources: [{ cluster: "prd", origin: "GitRepository", namespace: "flux-system", name: "fleet", url: "https://github.com/acme/fleet" }],
    tracked: ["refs/heads/main"],
    trackNotes: [],
    size: 3 << 20,
    fetchedAt: "2026-10-15T20:31:00Z",
    needsRepair: false,
    fetching: false,
    ...over,
  };
}

function state(over: Partial<GitSettings> = {}): GitSettings {
  return {
    path: "/x/fleet.yaml",
    binary: { ready: true, path: "/opt/homebrew/bin/git", version: "git version 2.56.0" },
    helpers: ["osxkeychain"],
    refusedHelpers: 0,
    repos: [repo()],
    unapproved: [
      {
        key: "git.internal/infra",
        serves: "git.internal/infra",
        sources: [{ cluster: "stg", origin: "GitRepository", namespace: "flux-system", name: "infra", url: "https://git.internal/infra.git" }],
        refused: "only https and ssh run, not \"http\"",
      },
      {
        key: "github.com/acme/tools",
        serves: "github.com/acme/tools",
        sources: [{ cluster: "stg", origin: "Application", namespace: "argocd", name: "tools", url: "https://github.com/acme/tools.git" }],
        suggest: "https://github.com/acme/tools.git",
      },
    ],
    notes: [],
    ...over,
  };
}

const ok = { ok: true, error: "" };

beforeEach(() => {
  for (const f of Object.values(bridge)) f.mockReset();
  for (const f of [bridge.approveGitRepo, bridge.removeGitRepo, bridge.setGitRepoMatch, bridge.fetchGitRepo, bridge.clearGitCache]) f.mockResolvedValue(ok);
});

describe("GitSettingsPanel", () => {
  it("names the git in use, the approved URL exactly, its history and what it tracks", async () => {
    bridge.getGitSettings.mockResolvedValue(state());
    render(<GitSettingsPanel />);
    expect((await screen.findByTestId("git-binary")).textContent).toBe("Runs git 2.56.0 at /opt/homebrew/bin/git");
    const r = screen.getByTestId("git-repo");
    expect(within(r).getByText("git@github.com:acme/fleet.git")).toBeTruthy();
    expect(within(r).getByTestId("git-history").textContent).toMatch(/^history as of .+ · 3\.0 MiB$/);
    expect(within(r).getByText("refs/heads/main")).toBeTruthy();
    expect(within(r).getByText("https://github.com/acme/fleet")).toBeTruthy();
    expect(screen.getByText(/your credential helper \(osxkeychain\)/)).toBeTruthy();
  });

  it("keeps the last good history beside a failed fetch, and says when a cache needs repair", () => {
    expect(historyLine(repo({ error: "fetch failed: 403" })).text).toMatch(/^history as of .+; fetch failed: fetch failed: 403$/);
    expect(historyLine(repo({ fetchedAt: undefined, error: "timed out" })).text).toBe("fetch failed: timed out");
    expect(historyLine(repo({ needsRepair: true })).text).toContain("Clear cache");
    expect(historyLine(repo({ fetchedAt: undefined })).text).toBe("not fetched yet");
  });

  it("says why Git is off and fetches nothing", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ binary: { ready: true, off: "git 2.44.0 is older than 2.45, which partial-clone safety needs" } }));
    render(<GitSettingsPanel />);
    expect((await screen.findByTestId("git-binary")).textContent).toContain("Git features are off: git 2.44.0 is older than 2.45");
    expect((screen.getByRole("button", { name: "Fetch now" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("fetches now and reports the result", async () => {
    bridge.getGitSettings.mockResolvedValue(state());
    bridge.fetchGitRepo.mockResolvedValue({ ok: false, error: "fetch failed: Permission denied (publickey)" });
    render(<GitSettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Fetch now" }));
    await waitFor(() => expect(bridge.fetchGitRepo).toHaveBeenCalledWith("git@github.com:acme/fleet.git"));
    expect((await screen.findByTestId("git-message")).textContent).toContain("Permission denied (publickey)");
  });

  it("cannot fetch a repository with nothing tracked, and says why", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ repos: [repo({ tracked: [], trackNotes: ["the line's key (semver >=1.0): a semver range has no single target"] })] }));
    render(<GitSettingsPanel />);
    expect(((await screen.findByRole("button", { name: "Fetch now" })) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/a semver range has no single target/)).toBeTruthy();
  });

  it("approves exactly the URL in the dialog, serving the clusters' repository", async () => {
    bridge.getGitSettings.mockResolvedValue(state());
    render(<GitSettingsPanel />);
    const items = await screen.findAllByTestId("git-unapproved");
    // The http URL cannot run as written: the dialog starts empty and says why.
    fireEvent.click(within(items[0]).getByRole("button", { name: "Approve…" }));
    let dialog = screen.getByRole("dialog");
    expect((within(dialog).getByLabelText("URL to approve") as HTMLInputElement).value).toBe("");
    expect(within(dialog).getByText(/cannot run as written/)).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Approve" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText("URL to approve"), { target: { value: "infra-git:platform/infra.git" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(bridge.approveGitRepo).toHaveBeenCalledWith("infra-git:platform/infra.git", "", "git.internal/infra"));

    // A suggestable URL is prefilled; what is approved is what the field holds.
    fireEvent.click(within(screen.getAllByTestId("git-unapproved")[1]).getByRole("button", { name: "Approve…" }));
    dialog = screen.getByRole("dialog");
    expect((within(dialog).getByLabelText("URL to approve") as HTMLInputElement).value).toBe("https://github.com/acme/tools.git");
    fireEvent.change(within(dialog).getByLabelText("Open in Git link"), { target: { value: "https://github.com/acme/tools" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(bridge.approveGitRepo).toHaveBeenLastCalledWith("https://github.com/acme/tools.git", "https://github.com/acme/tools", "github.com/acme/tools"));
  });

  it("adds and removes an explicit match, and removes an approval after confirming", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ repos: [repo({ matches: ["github.com/acme/old"] })] }));
    render(<GitSettingsPanel />);
    fireEvent.change(await screen.findByLabelText("another repository git@github.com:acme/fleet.git serves"), { target: { value: " github.com/acme/fleet-mirror " } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(bridge.setGitRepoMatch).toHaveBeenCalledWith("git@github.com:acme/fleet.git", "github.com/acme/fleet-mirror", true));
    fireEvent.click(await screen.findByRole("button", { name: "stop serving github.com/acme/old" }));
    await waitFor(() => expect(bridge.setGitRepoMatch).toHaveBeenLastCalledWith("git@github.com:acme/fleet.git", "github.com/acme/old", false));

    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    expect(bridge.removeGitRepo).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("deletes its cache (3.0 MiB)");
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.removeGitRepo).toHaveBeenCalledWith("git@github.com:acme/fleet.git"));
  });

  it("notes refused helpers and ignored fleet.yaml entries", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ refusedHelpers: 1, notes: ["git.repos[2] https://github.com/acme/leak.git is ignored, never fetched: an https URL must not carry a user or token"] }));
    render(<GitSettingsPanel />);
    expect((await screen.findByTestId("git-refused-helpers")).textContent).toContain("shell snippet");
    expect(screen.getByTestId("git-note").textContent).toContain("never fetched");
  });
});

describe("GitSettingsPanel without a repository key", () => {
  it("approves with no serves when the clusters' URL names no repository", async () => {
    bridge.getGitSettings.mockResolvedValue(state({
      unapproved: [{ key: "(an unrecognised URL)", serves: "", sources: [{ cluster: "stg", origin: "Application", namespace: "argocd", name: "x", url: "(an unrecognised URL)" }], refused: "not a parsable URL" }],
    }));
    render(<GitSettingsPanel />);
    fireEvent.click(within(await screen.findByTestId("git-unapproved")).getByRole("button", { name: "Approve…" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("serves only its own");
    fireEvent.change(within(dialog).getByLabelText("URL to approve"), { target: { value: "git@github.com:acme/x.git" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(bridge.approveGitRepo).toHaveBeenCalledWith("git@github.com:acme/x.git", "", ""));
  });

  // Settings review slice 0: a failed reread qualifies what is shown.
  it("qualifies what is shown when a reread fails", async () => {
    bridge.getGitSettings.mockResolvedValueOnce(state()).mockResolvedValue(null);
    bridge.fetchGitRepo.mockResolvedValue(ok);
    render(<GitSettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Fetch now" }));
    expect((await screen.findByTestId("git-reread-failed")).textContent).toContain("as last read");
  });
});
