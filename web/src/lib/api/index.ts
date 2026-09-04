import type { HarnessApi } from "./types";
import { httpApi } from "./http";
import { mockApi } from "./mock";

// The one place the implementation is chosen. VITE_MOCK=1 swaps the in memory
// harness in, so the interface runs with no backend and no other file changes.
export const api: HarnessApi = import.meta.env.VITE_MOCK === "1" ? mockApi : httpApi;

export { ApiError } from "./http";
export * from "./types";
