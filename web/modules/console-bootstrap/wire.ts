export interface ConsoleMetadataWire {
  operator: {
    name: string
  }
  build: {
    version: string
    commit: string
    built_at: string
  }
  capabilities: {
    blackboard_history: boolean
    execution_activity: boolean
  }
}
