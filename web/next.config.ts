import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

// A static export under /setup, served by hermes-setup (the Go program)
// from its embedded copy of out/. In development the API is the program
// running on 8080.
export default function config(phase: string): NextConfig {
  const base = { basePath: "/setup", agentRules: false, transpilePackages: ["@shpyrd/ui"] };
  if (phase === PHASE_DEVELOPMENT_SERVER) {
    return {
      ...base,
      rewrites: async () => [{ source: "/setup/api/:path*", destination: "http://localhost:8080/setup/api/:path*" }],
    };
  }
  return { ...base, output: "export" };
}
