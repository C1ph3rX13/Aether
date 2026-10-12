import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, expect, it, vi } from "vitest"
import { useMemo, useState } from "react"
import {
  NavigationGuardProvider,
  useNavigationActions,
  useNavigationGuard,
} from "@/components/navigation-guard"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
beforeEach(() => vi.stubGlobal("navigation", new EventTarget()))

function Editor({ save }: { save: () => Promise<boolean> }) {
  const [dirty, setDirty] = useState(false)
  const [created, setCreated] = useState(false)
  useNavigationGuard(
    useMemo(
      () => ({
        isDirty: () => dirty,
        isBusy: () => false,
        busy: false,
        save,
        discard: () => setDirty(false),
      }),
      [dirty, save]
    )
  )
  const { requestAction } = useNavigationActions()
  return (
    <>
      <Input aria-label="Setting" onChange={() => setDirty(true)} />
      <Button
        onClick={(event) =>
          requestAction(() => setCreated(true), event.currentTarget)
        }
      >
        New Project
      </Button>
      {created && <p>Creation opened</p>}
    </>
  )
}

it("handles Cancel and a failed Save before allowing the global creation action", async () => {
  const user = userEvent.setup()
  const save = vi.fn().mockResolvedValue(false)
  render(
    <NavigationGuardProvider>
      <Editor save={save} />
    </NavigationGuardProvider>
  )
  fireEvent.change(screen.getByLabelText("Setting"), {
    target: { value: "edited" },
  })
  const trigger = screen.getByRole("button", { name: "New Project" })
  await user.click(trigger)
  const dialog = screen.getByRole("dialog", { name: "Unsaved settings" })
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
  expect(screen.queryByText("Creation opened")).not.toBeInTheDocument()
  expect(trigger).toHaveFocus()
  await user.click(trigger)
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Save" })
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(save).toHaveBeenCalledOnce()
  expect(screen.queryByText("Creation opened")).not.toBeInTheDocument()
  expect(screen.getByLabelText("Setting")).toHaveValue("edited")
  await user.click(trigger)
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Discard" })
  )
  expect(await screen.findByText("Creation opened")).toBeVisible()
})
