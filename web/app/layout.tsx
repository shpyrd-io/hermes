import type { Metadata } from "next";
import "@/styles/global.css";

export const metadata: Metadata = {
  title: "Set up Hermes",
  description: "Where Hermes' model comes from, and the key.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        {/* The theme is applied before the first paint, by a file of its own. */}
        {/* eslint-disable-next-line @next/next/no-sync-scripts */}
        <script src="/setup/theme.js" />
      </head>
      <body className="bg-background text-foreground">{children}</body>
    </html>
  );
}
