import { getProjectLog, getRevisionBlackboard } from "@/modules/log/api"
import type { ConsoleLogPage } from "@/modules/log/model"
import type { ReplayFrame } from "@/modules/replay/model"

const REPLAY_REQUEST_CONCURRENCY = 4

export async function loadReplayWindow(
  projectId: string,
  fromRevision: number,
  toRevision: number,
  signal?: AbortSignal
): Promise<ReplayFrame[]> {
  if (
    !Number.isSafeInteger(fromRevision) ||
    !Number.isSafeInteger(toRevision) ||
    fromRevision < 1 ||
    fromRevision > toRevision
  ) {
    throw new Error("Replay revision window is invalid")
  }
  const controller = new AbortController()
  const readSignal = signal
    ? AbortSignal.any([signal, controller.signal])
    : controller.signal
  const frames: ReplayFrame[] = []
  let nextRevision = fromRevision
  const loadFrames = async () => {
    try {
      while (nextRevision <= toRevision) {
        readSignal.throwIfAborted()
        const revision = nextRevision++
        const project = await getRevisionBlackboard(
          projectId,
          revision,
          readSignal
        )
        readSignal.throwIfAborted()
        if (project.project.revision !== revision) {
          throw new Error(
            `Replay snapshot Revision mismatch at Revision ${revision}`
          )
        }
        if (project.project.id !== projectId) {
          throw new Error(
            `Replay snapshot Project mismatch at Revision ${revision}`
          )
        }
        frames[revision - fromRevision] = { revision, project }
      }
    } catch (error) {
      controller.abort(error)
      throw error
    }
  }
  await Promise.all(
    Array.from(
      {
        length: Math.min(
          REPLAY_REQUEST_CONCURRENCY,
          toRevision - fromRevision + 1
        ),
      },
      loadFrames
    )
  )
  readSignal.throwIfAborted()
  return frames
}

export async function loadFrozenReplay(
  projectId: string,
  toRevision: number,
  signal: AbortSignal
): Promise<{ frames: ReplayFrame[]; log: ConsoleLogPage }> {
  const controller = new AbortController()
  const readSignal = AbortSignal.any([signal, controller.signal])
  try {
    const first = await getProjectLog(
      projectId,
      toRevision + 1,
      100,
      readSignal
    )
    const fromRevision = first.availableFromRevision
    const loadLog = async () => {
      const groups = new Map(
        first.revisions.map((group) => [group.revision, group])
      )
      let before = first.nextBeforeRevision
      while (before !== undefined && before > fromRevision) {
        const page = await getProjectLog(projectId, before, 100, readSignal)
        readSignal.throwIfAborted()
        for (const group of page.revisions) groups.set(group.revision, group)
        if (
          page.nextBeforeRevision !== undefined &&
          page.nextBeforeRevision >= before
        )
          throw new Error("Replay Log did not advance")
        before = page.nextBeforeRevision
      }
      const revisions = [...groups.values()]
        .filter(
          (group) =>
            group.revision >= fromRevision && group.revision <= toRevision
        )
        .sort((a, b) => a.revision - b.revision)
      if (
        revisions.length !== toRevision - fromRevision + 1 ||
        revisions.some(
          (group, index) => group.revision !== fromRevision + index
        )
      )
        throw new Error("Replay Log window is incomplete")
      return {
        ...first,
        currentRevision: toRevision,
        currentRevisionOccurredAt: revisions.at(-1)!.occurredAt,
        revisions,
        nextBeforeRevision: undefined,
      }
    }
    const [frames, log] = await Promise.all([
      loadReplayWindow(projectId, fromRevision, toRevision, readSignal),
      loadLog(),
    ])
    readSignal.throwIfAborted()
    return { frames, log }
  } catch (error) {
    controller.abort(error)
    throw error
  }
}
