import { screen } from "@testing-library/react"
import type { UserEvent } from "@testing-library/user-event"

/** Exercise the visible menu rather than its hidden native form input. */
export async function selectOption(
  user: UserEvent,
  trigger: HTMLElement,
  label: string
) {
  await user.click(trigger)
  await user.click(screen.getByRole("option", { name: label }))
}
