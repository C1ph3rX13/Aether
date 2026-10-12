import type { NextConfig } from "next"
import { PHASE_DEVELOPMENT_SERVER } from "next/constants"

function nextConfig(phase: string): NextConfig {
  if (phase === PHASE_DEVELOPMENT_SERVER)
    return {
      compress: false,
      async rewrites() {
        return [
          {
            source: "/api/:path*",
            destination: "http://localhost:8000/api/:path*",
          },
          {
            source: "/projects/:id",
            destination: "/projects/__placeholder__",
          },
        ]
      },
    }
  return { output: "export", compress: false }
}

export default nextConfig
