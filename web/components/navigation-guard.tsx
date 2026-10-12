"use client"
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import type { MouseEvent, ReactNode } from "react"
import { useRouter } from "next/navigation"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
interface NavigationGuard {
  isDirty?: () => boolean
  isBusy: () => boolean
  busy?: boolean
  save?: () => Promise<boolean>
  discard?: () => void
}
interface NavigationGuardContextValue {
  register: (guard: NavigationGuard | null) => void
  busy: boolean
  isBusy: () => boolean
  requestAction: (action: () => void, finalFocus: HTMLElement | null) => void
  requestNavigation: (href: string, finalFocus: HTMLElement | null) => void
}
interface PendingNavigation {
  href: string
  historyKey?: string
  external?: boolean
}
interface PendingCommand {
  action: () => void
}
const NavigationGuardContext =
  createContext<NavigationGuardContextValue | null>(null)
export function NavigationGuardProvider({ children }: { children: ReactNode }) {
  const router = useRouter()
  const guardRef = useRef<NavigationGuard | null>(null)
  const allowedHistoryKey = useRef<string | null>(null)
  const saveInFlight = useRef(false)
  const [busy, setBusy] = useState(false)
  const afterClose = useRef<(() => void) | null>(null)
  const restoreFocus = useRef<HTMLElement | null>(null)
  const [pendingNavigation, setPendingNavigation] = useState<
    PendingNavigation | PendingCommand | null
  >(null)
  const [pendingAction, setPendingAction] = useState<"save" | "discard" | null>(
    null
  )
  const register = useCallback((guard: NavigationGuard | null) => {
    guardRef.current = guard
    setBusy(guard?.busy ?? false)
  }, [])
  const isBusy = useCallback(
    () => saveInFlight.current || Boolean(guardRef.current?.isBusy()),
    []
  )
  const requestAction = useCallback(
    (action: () => void, finalFocus: HTMLElement | null) => {
      if (isBusy()) return
      if (guardRef.current?.isDirty?.()) {
        restoreFocus.current = finalFocus
        setPendingNavigation({ action })
      } else action()
    },
    [isBusy]
  )
  const requestNavigation = useCallback(
    (href: string, finalFocus: HTMLElement | null) => {
      if (isBusy()) return
      if (guardRef.current?.isDirty?.()) {
        restoreFocus.current = finalFocus
        setPendingNavigation({ href })
      } else router.push(href)
    },
    [isBusy, router]
  )
  const handleClickCapture = useCallback(
    (event: MouseEvent<HTMLDivElement>) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      )
        return
      const target = event.target
      if (!(target instanceof Element)) return
      const anchor = target.closest("a[href]")
      if (!(anchor instanceof HTMLAnchorElement)) return
      const href = anchor.getAttribute("href")
      if (
        !href ||
        !href.startsWith("/") ||
        href.startsWith("//") ||
        href === window.location.pathname
      )
        return
      const guard = guardRef.current
      if (!guard || (!guard.isBusy() && !guard.isDirty?.())) return
      event.preventDefault()
      event.stopPropagation()
      if (guard.isBusy()) return
      setPendingNavigation({ href })
    },
    []
  )
  useEffect(() => {
    const handleNavigate = (event: NavigateEvent) => {
      if (event.navigationType !== "traverse" || !event.cancelable) return
      if (
        allowedHistoryKey.current !== null &&
        event.destination.key === allowedHistoryKey.current
      ) {
        allowedHistoryKey.current = null
        return
      }
      const guard = guardRef.current
      if (!guard || (!guard.isBusy() && !guard.isDirty?.())) return
      // Cancel before Next.js handles popstate; the history stack stays intact.
      event.preventDefault()
      if (!guard.isBusy()) {
        const targetURL = new URL(event.destination.url)
        setPendingNavigation({
          href: event.destination.sameDocument
            ? `${targetURL.pathname}${targetURL.search}${targetURL.hash}`
            : event.destination.url,
          historyKey: event.destination.key ?? undefined,
          external: !event.destination.sameDocument,
        })
      }
    }
    const navigation = window.navigation
    navigation.addEventListener("navigate", handleNavigate)
    return () => navigation.removeEventListener("navigate", handleNavigate)
  }, [])
  const continueNavigation = useCallback(() => {
    if (!pendingNavigation) return
    setPendingNavigation(null)
    if ("action" in pendingNavigation) {
      afterClose.current = pendingNavigation.action
      return
    }
    if (pendingNavigation.historyKey) {
      allowedHistoryKey.current = pendingNavigation.historyKey
      window.navigation.traverseTo(pendingNavigation.historyKey)
    } else if (pendingNavigation.external) {
      window.location.assign(pendingNavigation.href)
    } else router.push(pendingNavigation.href)
  }, [pendingNavigation, router])
  const saveAndLeave = useCallback(async () => {
    const guard = guardRef.current
    if (!guard?.save || !pendingNavigation || saveInFlight.current) return
    saveInFlight.current = true
    setPendingAction("save")
    const saved = await guard.save()
    saveInFlight.current = false
    setPendingAction(null)
    if (saved) continueNavigation()
    else setPendingNavigation(null)
  }, [continueNavigation, pendingNavigation])
  const discardAndLeave = useCallback(() => {
    const guard = guardRef.current
    if (!guard?.discard || !pendingNavigation) return
    setPendingAction("discard")
    guard.discard()
    setPendingAction(null)
    continueNavigation()
  }, [continueNavigation, pendingNavigation])
  return (
    <NavigationGuardContext.Provider
      value={useMemo(
        () => ({ register, busy, isBusy, requestAction, requestNavigation }),
        [register, busy, isBusy, requestAction, requestNavigation]
      )}
    >
      <div className="min-h-svh" onClickCapture={handleClickCapture}>
        {children}
      </div>
      <Dialog
        open={pendingNavigation !== null}
        onOpenChange={(open) => {
          if (!open && pendingAction === null) setPendingNavigation(null)
        }}
      >
        <DialogContent
          showCloseButton={false}
          onPointerDownOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (pendingAction !== null) event.preventDefault()
          }}
          onCloseAutoFocus={(event) => {
            const action = afterClose.current
            const destination = restoreFocus.current
            afterClose.current = null
            restoreFocus.current = null
            if (action || destination) {
              event.preventDefault()
              if (action) action()
              else destination?.focus()
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>Unsaved settings</DialogTitle>
            <DialogDescription>
              {pendingNavigation && "action" in pendingNavigation
                ? "Save your changes before creating a Project?"
                : "Save your changes before leaving this page?"}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={pendingAction !== null}
              onClick={() => setPendingNavigation(null)}
            >
              Cancel
            </Button>
            <Button
              variant="outline"
              disabled={pendingAction !== null}
              onClick={discardAndLeave}
            >
              {pendingAction === "discard" ? "Discarding..." : "Discard"}
            </Button>
            <Button
              disabled={pendingAction !== null}
              onClick={() => void saveAndLeave()}
            >
              {pendingAction === "save" ? "Saving..." : "Save"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </NavigationGuardContext.Provider>
  )
}
export function useNavigationActions() {
  const context = useContext(NavigationGuardContext)
  if (!context)
    throw new Error(
      "useNavigationActions must be used inside NavigationGuardProvider"
    )
  return context
}
export function useNavigationGuard(guard: NavigationGuard | null) {
  const context = useContext(NavigationGuardContext)
  if (!context)
    throw new Error(
      "useNavigationGuard must be used inside NavigationGuardProvider"
    )
  const { register } = context
  useEffect(() => {
    register(guard)
    return () => register(null)
  }, [register, guard])
}
