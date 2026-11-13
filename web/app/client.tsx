"use client";

import dynamic from "next/dynamic";

const App = dynamic(() => import("@/app/App"), { ssr: false });

export function ClientOnly() {
  return <App />;
}
