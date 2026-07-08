import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";

// Flat config (ESLint 9). A tight, high-signal set - the same philosophy as
// .golangci.yml. Recommended JS + TS rules, plus the two CLASSIC react-hooks
// bug-catchers only:
//   - rules-of-hooks (conditional/looped hooks = real bug)         -> error
//   - exhaustive-deps (stale closures)                             -> warn
// The rest of react-hooks v7 "recommended" is the React Compiler static-analysis
// set (refs / set-state-in-effect / purity / manual-memoization). Those presume
// Compiler adoption and flag idiomatic, correct effects/refs across this
// hand-optimized codebase; enabling them would flood without catching defects.
// react-refresh (HMR-only) is omitted for the same reason - this codebase
// deliberately co-locates helpers with components.
export default tseslint.config(
  { ignores: ["dist/**", "bindings/**"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      globals: { ...globals.browser },
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
    },
  },
  {
    // Tests: pragmatic `any` in fixtures/casts carries no defect risk.
    files: ["src/**/*.test.{ts,tsx}", "src/**/*.spec.{ts,tsx}"],
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
    },
  },
);
