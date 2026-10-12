import { Geist, Geist_Mono } from "next/font/google"
import type { Metadata, Viewport } from "next"

import "./globals.css"
import { OperationsRuntime } from "@/components/operations-runtime"
import { NavigationGuardProvider } from "@/components/navigation-guard"
import { TooltipProvider } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

const geist = Geist({ subsets: ["latin"], variable: "--font-sans" })

const fontMono = Geist_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
})

export const metadata: Metadata = {
  title: "Aether",
  description: "Aether Blackboard Operations",
}

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
}

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode
}>) {
  return (
    <html
      lang="en"
      className={cn(
        "antialiased",
        fontMono.variable,
        "font-sans",
        geist.variable
      )}
    >
      <body>
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:fixed focus:start-3 focus:top-3 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2 focus:text-sm focus:ring-2"
        >
          Skip to main content
        </a>
        <TooltipProvider>
          <NavigationGuardProvider>
            <OperationsRuntime>{children}</OperationsRuntime>
          </NavigationGuardProvider>
        </TooltipProvider>
      </body>
    </html>
  )
}
