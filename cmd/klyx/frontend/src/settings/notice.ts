import { create } from "zustand";

// A notice Settings shows once at the top of the page: a restored route
// whose section is unknown, or an anchor that is no longer there (S1 §1).
export const useSettingsNotice = create<{ notice: string | null }>(() => ({ notice: null }));

export function sayInSettings(notice: string | null): void {
  useSettingsNotice.setState({ notice });
}
