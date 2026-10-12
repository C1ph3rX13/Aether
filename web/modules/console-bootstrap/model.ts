import type { ConsoleMetadataWire } from "@/modules/console-bootstrap/wire"

export interface ConsoleMetadata {
  operator: {
    name: string
  }
  build: {
    version: string
    commit: string
    builtAt: string
  }
  capabilities: {
    blackboardHistory: boolean
    executionActivity: boolean
  }
}

export function consoleMetadataFromWire(
  metadata: ConsoleMetadataWire
): ConsoleMetadata {
  return {
    operator: metadata.operator,
    build: {
      version: metadata.build.version,
      commit: metadata.build.commit,
      builtAt: metadata.build.built_at,
    },
    capabilities: {
      blackboardHistory: metadata.capabilities.blackboard_history,
      executionActivity: metadata.capabilities.execution_activity,
    },
  }
}
