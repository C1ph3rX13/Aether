import { describe, expect, it, vi } from "vitest"
import { executeProjectCommand, type ProjectCommandAuthority } from "./gateway"

const liveAuthority: ProjectCommandAuthority = {
  project: {
    id: "proj-1",
    title: "Plan",
    status: "active",
    revision: 3,
    generation: 1,
    bootstrapEnabled: false,
    createdAt: "2026-01-01T00:00:00Z",
  },
  freshness: "live",
  mode: "live",
  markCommandCommitted: vi.fn(),
}

describe("Project command authority", () => {
  it.each(["paused", "completed"] as const)(
    "rejects exhausted-work retry for %s before HTTP",
    async (status) => {
      const command = vi.fn()
      await expect(
        executeProjectCommand(
          {
            ...liveAuthority,
            project: { ...liveAuthority.project!, status },
          },
          "retryExhaustedWork",
          command
        )
      ).rejects.toThrow("not available")
      expect(command).not.toHaveBeenCalled()
    }
  )
  it.each(["stale", "refreshing", "reconnecting", "replay"] as const)(
    "rejects writes while %s without sending a request",
    async (freshness) => {
      const command = vi.fn()
      const markCommandCommitted = vi.fn()
      await expect(
        executeProjectCommand(
          { ...liveAuthority, freshness, markCommandCommitted },
          "addHint",
          command
        )
      ).rejects.toMatchObject({ code: "projection_not_live", status: 409 })
      expect(command).not.toHaveBeenCalled()
      expect(markCommandCommitted).not.toHaveBeenCalled()
    }
  )
  it("rejects Replay even if the latest live projection has caught up", async () => {
    const command = vi.fn()
    await expect(
      executeProjectCommand(
        { ...liveAuthority, mode: "replay" },
        "addHint",
        command
      )
    ).rejects.toMatchObject({ code: "projection_not_live" })
    expect(command).not.toHaveBeenCalled()
  })
  it("rejects completion for a Paused Project before HTTP", async () => {
    const command = vi.fn()
    await expect(
      executeProjectCommand(
        {
          ...liveAuthority,
          project: { ...liveAuthority.project!, status: "paused" },
        },
        "completeProject",
        command
      )
    ).rejects.toThrow("not available")
    expect(command).not.toHaveBeenCalled()
  })
  it("waits for the authoritative receipt Revision", async () => {
    const receipt = { project_revision: 9 }
    const markCommandCommitted = vi.fn()
    expect(
      await executeProjectCommand(
        { ...liveAuthority, markCommandCommitted },
        "addHint",
        async () => receipt
      )
    ).toEqual(receipt)
    expect(markCommandCommitted).toHaveBeenCalledWith(9)
  })
  it("propagates command failure without announcing a committed revision", async () => {
    const markCommandCommitted = vi.fn()
    const failure = new Error("Conflict")
    await expect(
      executeProjectCommand(
        { ...liveAuthority, markCommandCommitted },
        "addHint",
        async () => {
          throw failure
        }
      )
    ).rejects.toBe(failure)
    expect(markCommandCommitted).not.toHaveBeenCalled()
  })
  it.each([undefined, 0, 1.5, "9"])(
    "rejects an unreadable receipt Revision %s without inventing a target",
    async (revision) => {
      const markCommandCommitted = vi.fn()
      await expect(
        executeProjectCommand(
          { ...liveAuthority, markCommandCommitted },
          "addHint",
          async () =>
            ({ project_revision: revision }) as { project_revision: number }
        )
      ).rejects.toThrow("unreadable Revision")
      expect(markCommandCommitted).not.toHaveBeenCalled()
    }
  )
})
