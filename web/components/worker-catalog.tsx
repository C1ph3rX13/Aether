"use client"
import { useRef, useState } from "react"
import Link from "next/link"
import {
  IconAlertCircle as CircleAlert,
  IconPlus as Plus,
  IconRefresh as RefreshCw,
} from "@tabler/icons-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { FieldDescription } from "@/components/ui/field"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { WorkerCard } from "@/components/worker-card"
import { Skeleton } from "@/components/ui/skeleton"
import { ConfigurationProblems } from "@/components/configuration-problems"
import { useWorkerCatalog } from "@/hooks/use-worker-catalog"
import {
  workerBranches,
  workerIcons,
  workerNeedsSetup,
  type AgentType,
} from "@/lib/worker-catalog"
export function WorkerCatalog({ agentType }: { agentType: AgentType }) {
  const {
    read,
    failed,
    loading,
    load,
    deleteWorker,
    deletePending,
    deleteFailure,
  } = useWorkerCatalog()
  const [selected, setSelected] = useState<string | null>(null)
  const triggerRef = useRef<HTMLElement | null>(null)
  const refreshRef = useRef<HTMLButtonElement | null>(null)
  const cancelRef = useRef<HTMLButtonElement | null>(null)
  const branch = workerBranches[agentType]
  const Icon = workerIcons[agentType]
  const workers = read?.draft.workers
    .map((worker, index) => ({ worker, index }))
    .filter(({ worker }) => worker.agent_type === agentType)
  const deletingWorker = workers?.find(
    ({ worker }) => worker.name === selected
  )?.worker
  const deleteMessage =
    deleteFailure === "conflict"
      ? "Worker settings changed elsewhere. Reload the list and confirm deletion again."
      : deleteFailure === "validation"
        ? "Worker deletion was rejected. Reload the list to review saved settings, then try again."
        : "Worker could not be deleted. Check your connection and retry, or reload the list to check saved settings."
  const reload = async () => {
    if (await load()) setSelected(null)
  }
  return (
    <div className="@container/workers flex flex-1 flex-col gap-6 p-4 lg:p-6">
      {read && <ConfigurationProblems problems={read.validation_problems} />}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 flex-1 basis-80">
          <h2 className="flex items-center gap-2 text-2xl font-semibold">
            <Icon className="size-6" aria-hidden="true" />
            {branch.label} Workers
          </h2>
          <p className="max-w-prose text-sm text-muted-foreground">
            {workers?.length ?? 0} configured Workers
          </p>
        </div>
        <div className="flex max-w-full min-w-0 flex-wrap gap-2">
          <Button
            role="link"
            disabled={loading || deletePending || selected !== null}
            asChild
          >
            <Link
              href={`${branch.href}/editor`}
              aria-disabled={
                loading || deletePending || selected !== null || undefined
              }
              tabIndex={
                loading || deletePending || selected !== null ? -1 : undefined
              }
              onClick={(event) => {
                if (loading || deletePending || selected !== null)
                  event.preventDefault()
              }}
            >
              <Plus aria-hidden="true" data-icon="inline-start" />
              New Worker
            </Link>
          </Button>
          <Button
            ref={refreshRef}
            variant="outline"
            disabled={loading || deletePending || selected !== null}
            onClick={() => void reload()}
          >
            <RefreshCw aria-hidden="true" data-icon="inline-start" />
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
        </div>
      </div>
      {failed && (
        <Alert role="alert">
          <CircleAlert aria-hidden="true" />
          <AlertTitle>Unable to load Workers</AlertTitle>
          <AlertDescription>
            {read
              ? "Showing last loaded Workers. Reload before deleting."
              : "Retry to load saved Workers."}
            <Button
              variant="outline"
              size="default"
              disabled={loading || deletePending}
              onClick={() => void reload()}
            >
              Retry
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {deleteFailure && selected === null && (
        <Alert role="alert">
          <CircleAlert aria-hidden="true" />
          <AlertTitle>Worker deletion unavailable</AlertTitle>
          <AlertDescription>{deleteMessage}</AlertDescription>
        </Alert>
      )}
      {!read ? (
        !failed && (
          <div
            role="status"
            aria-label="Loading Workers"
            className="grid gap-4 @min-[40rem]/workers:grid-cols-2 @min-[60rem]/workers:grid-cols-3"
          >
            <FieldDescription className="col-span-full hidden motion-reduce:block">
              Loading Workers…
            </FieldDescription>
            <Skeleton className="h-56" />
            <Skeleton className="h-56" />
          </div>
        )
      ) : workers?.length ? (
        <div
          role="list"
          aria-label={`${branch.label} Workers`}
          className="grid auto-rows-fr grid-cols-[repeat(auto-fill,minmax(min(100%,18rem),1fr))] gap-4"
        >
          {workers.map(({ worker, index }) => (
            <WorkerCard
              key={worker.name}
              worker={worker}
              needsSetup={workerNeedsSetup(read, index)}
              disabled={loading || deletePending || selected !== null}
              deleteDisabled={failed || deleteFailure === "conflict"}
              onDelete={(trigger) => {
                triggerRef.current = trigger
                setSelected(worker.name)
              }}
            />
          ))}
        </div>
      ) : (
        <Empty className="min-h-56 border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Icon aria-hidden="true" />
            </EmptyMedia>
            <EmptyTitle>No {branch.label} Workers</EmptyTitle>
            <EmptyDescription>
              No saved {branch.label} Workers.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      <Dialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open && (deletePending || loading)) {
            return
          }
          if (!open) setSelected(null)
        }}
      >
        <DialogContent
          showCloseButton={false}
          onPointerDownOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (deletePending || loading) event.preventDefault()
          }}
          onCloseAutoFocus={(event) => {
            const destination =
              triggerRef.current?.isConnected &&
              triggerRef.current.getAttribute("aria-disabled") !== "true"
                ? triggerRef.current
                : refreshRef.current
            if (destination) {
              event.preventDefault()
              destination.focus()
            }
          }}
          onOpenAutoFocus={(event) => {
            event.preventDefault()
            cancelRef.current?.focus()
          }}
        >
          <DialogHeader>
            <DialogTitle className="wrap-anywhere">
              Delete {selected}?
            </DialogTitle>
            <DialogDescription>
              This removes the Worker from the saved configuration. Existing
              Projects keep their bound configuration. Saving changes affects
              new Projects.
            </DialogDescription>
          </DialogHeader>
          {deleteFailure && (
            <Alert role="alert">
              <AlertTitle>Worker could not be deleted</AlertTitle>
              <AlertDescription>{deleteMessage}</AlertDescription>
            </Alert>
          )}
          {failed && (
            <p role="alert">Could not reload Workers. Retry Reload list.</p>
          )}
          <DialogFooter className="flex-wrap">
            {deleteFailure && (
              <Button
                variant="outline"
                disabled={loading || deletePending}
                onClick={() => void reload()}
              >
                {loading ? "Reloading…" : "Reload list"}
              </Button>
            )}
            <DialogClose asChild>
              <Button
                ref={cancelRef}
                variant="outline"
                disabled={deletePending || loading}
              >
                Cancel
              </Button>
            </DialogClose>
            <Button
              variant="destructive"
              disabled={
                deletePending ||
                loading ||
                failed ||
                !deletingWorker ||
                deleteFailure === "conflict"
              }
              onClick={async () => {
                if (deletingWorker && (await deleteWorker(deletingWorker.name)))
                  setSelected(null)
              }}
            >
              {deletePending ? "Deleting…" : "Delete Worker"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
