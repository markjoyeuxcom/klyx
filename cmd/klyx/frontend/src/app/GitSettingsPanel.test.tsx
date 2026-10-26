import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import type { GitRepo, GitSettings } from "../bridge/gitSettings";
import type { Plan } from "../bridge/plan";

const bridge = vi.hoisted(() => ({
  getGitSettings: vi.fn(),
  approveGitRepo: vi.fn(),
  removeGitRepo: vi.fn(),
  setGitRepoMatch: vi.fn(),
  fetchGitRepo: vi.fn(),
  clearGitCache: vi.fn(),
  planGitFromFile: vi.fn(),
  applyGit: vi.fn(),
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
    diverged: false,
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
    fireEvent.change(await screen.findByLabelText(/^Also serves \(/), { target: { value: " github.com/acme/fleet-mirror " } });
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

// Settings slice F2b: what is shown is the running copy; a hand edit is
// drift, adopted with "Use the file's values".
describe("GitSettingsPanel drift", () => {
  const filePlan = (over: Partial<Plan> = {}): Plan => ({
    section: "git", kind: "file", digest: "g1", fileVersion: "v1", runtime: { git: "0", work: "" },
    changes: [{ id: "git.repos[git@github.com:acme/fleet.git].url", label: "Repository git@github.com:acme/fleet.git", from: "approved", to: "not approved" }],
    ends: [], continues: [{ id: "fetch:git@github.com:acme/fleet.git", kind: "fetch", cluster: "", started: "2026-10-26T10:42:00Z", what: "the fetch of git@github.com:acme/fleet.git in progress: it finishes, and its history is no longer used" }],
    writes: false, activates: true, ...over,
  });

  it("offers the file's values as a plan, and applies only its digest", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ diverged: true }));
    bridge.planGitFromFile.mockResolvedValue(filePlan());
    bridge.applyGit.mockResolvedValue({ ok: true });
    render(<GitSettingsPanel />);
    const notice = await screen.findByTestId("git-diverged");
    expect(notice.textContent).toContain("differ from the ones in use");
    fireEvent.click(within(notice).getByRole("button", { name: "Use the file's values" }));
    const plan = await screen.findByTestId("settings-plan");
    expect(plan.getAttribute("data-plan-digest")).toBe("g1");
    expect(plan.textContent).toContain("fleet.yaml already says this");
    expect(plan.textContent).toContain("Continues");
    expect(plan.textContent).toContain("it finishes, and its history is no longer used");
    expect(bridge.applyGit).not.toHaveBeenCalled();
    bridge.getGitSettings.mockResolvedValue(state({ repos: [] }));
    fireEvent.click(within(plan).getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(bridge.applyGit).toHaveBeenCalledWith("g1"));
    await waitFor(() => expect(screen.queryByTestId("settings-plan")).toBeNull());
    expect(screen.queryByTestId("git-diverged")).toBeNull();
    expect(screen.getByTestId("git-message").textContent).toContain("Applied");
  });

  it("an action refused for drift says so; a plan it returns is shown, never applied", async () => {
    bridge.getGitSettings.mockResolvedValue(state({ diverged: true }));
    bridge.removeGitRepo.mockResolvedValue({ ok: false, error: "fleet.yaml's Git settings differ from the ones in use, so nothing was written: use the file's values first, or undo that edit" });
    render(<GitSettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    expect((await screen.findByTestId("git-message")).textContent).toContain("use the file's values first");

    bridge.setGitRepoMatch.mockResolvedValue({ ok: false, error: "Something changed since this was planned: review the new plan.", plan: filePlan({ kind: "save", digest: "g2", writes: true }) });
    fireEvent.change(screen.getByLabelText(/^Also serves \(/), { target: { value: "github.com/acme/fleet-mirror" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect((await screen.findByTestId("settings-plan")).getAttribute("data-plan-digest")).toBe("g2");
    expect(bridge.applyGit).not.toHaveBeenCalled();
  });
});

// F2b review, R2-R3: a lost answer is not a refusal, and a removal whose
// cache could not be cleared says so.
describe("GitSettingsPanel outcomes", () => {
  it("says a lost answer is not known, and reads again", async () => {
    bridge.getGitSettings.mockResolvedValue(state());
    bridge.removeGitRepo.mockResolvedValue({ ok: false, unknown: true, error: "lost bridge answer" });
    render(<GitSettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    const msg = await screen.findByTestId("git-message");
    expect(msg.textContent).toContain("whether it applied is not known");
    expect(msg.textContent).not.toContain("Removed");
    await waitFor(() => expect(bridge.getGitSettings).toHaveBeenCalledTimes(2));
  });

  it("says a removal whose cache could not be cleared", async () => {
    bridge.getGitSettings.mockResolvedValue(state());
    bridge.removeGitRepo.mockResolvedValue({ ok: true, note: "the approval was removed, but its cache could not be cleared: busy" });
    render(<GitSettingsPanel />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    expect((await screen.findByTestId("git-message")).textContent).toContain("could not be cleared");
  });
});

// F2b re-review, RR2: actions on different repositories may overlap; an
// older read or outcome never replaces a newer one.
describe("GitSettingsPanel overlapping actions", () => {
  it("keeps a newer readback and outcome over an older one's", async () => {
    const infra = repo({ url: "https://github.com/acme/infra.git", key: "github.com/acme/infra", sources: [] });
    let releaseRead!: (s: GitSettings) => void;
    let releaseFetch!: (r: { ok: boolean; error: string }) => void;
    bridge.getGitSettings
      .mockResolvedValueOnce(state({ repos: [repo(), infra] }))
      .mockImplementationOnce(() => new Promise<GitSettings>((r) => { releaseRead = r; }))
      .mockResolvedValue(state({ repos: [repo()] }));
    bridge.fetchGitRepo.mockImplementation(() => new Promise((r) => { releaseFetch = r; }));
    render(<GitSettingsPanel />);
    const rows = await screen.findAllByTestId("git-repo");
    // an older action on one repository, still running
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Fetch now" }));
    // a newer one on another, applied and read back
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.removeGitRepo).toHaveBeenCalledWith("https://github.com/acme/infra.git"));
    // the older action ends after it: its outcome is not said over the
    // newer's, and its readback, the newest read, answers
    releaseFetch({ ok: true, error: "" });
    await waitFor(() => expect(bridge.getGitSettings).toHaveBeenCalledTimes(3));
    expect(screen.getByTestId("git-message").textContent).toContain("Removed https://github.com/acme/infra.git");
    await waitFor(() => expect(screen.getAllByTestId("git-repo")).toHaveLength(1));
    // the removal's own readback answers last: an older read never
    // replaces what a newer one found
    releaseRead(state({ repos: [repo(), infra] }));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getAllByTestId("git-repo")).toHaveLength(1);
  });
});
