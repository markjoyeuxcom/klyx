import { beforeEach } from "vitest";

// View filters and the last route persist to localStorage/sessionStorage
// (src/store/persist.ts). jsdom shares one storage across a test file, so
// without this reset a search typed in one test would hydrate into the next
// test's mount - test-order coupling. Every test starts with clean storage.
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
});
