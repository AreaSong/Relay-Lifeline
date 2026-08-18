import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
		include: ["src/test/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: ["src/api.ts", "src/components/RepeatTaskDialog.tsx", "src/views/SettingsView.tsx"],
			thresholds: { lines: 35, functions: 15, branches: 25, statements: 30 },
    },
  },
});
