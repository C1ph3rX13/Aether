import type { ProjectDetail } from "@/modules/workspace/model"

export interface ReplayFrame {
  revision: number
  project: ProjectDetail
}

export interface ReplayControlState {
  active: boolean
  preparing: boolean
  restarting: boolean
  playing: boolean
  stepMs: number
  frameIndex: number
  frameRevisions: number[]
  fromRevision: number
  toRevision: number
  pendingCount: number
}

export interface ReplayControlActions {
  previous: () => void
  next: () => void
  selectFrame: (index: number) => void
  togglePlayback: () => void
  restart: () => Promise<void>
  setStepMs: (stepMs: number) => void
  exit: () => Promise<void>
}
