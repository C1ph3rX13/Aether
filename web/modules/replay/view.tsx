"use client"

import {
  IconChevronLeft as ChevronLeft,
  IconChevronRight as ChevronRight,
  IconPlayerPause as Pause,
  IconPlayerPlay as Play,
  IconRotate as RotateCcw,
  IconX as X,
} from "@tabler/icons-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Card, CardContent } from "@/components/ui/card"
import type { ReplayControlActions, ReplayControlState } from "./model"
import { formatCount } from "@/lib/format"

export function ReplayControl({
  replay,
  actions,
}: {
  replay: ReplayControlState
  actions: ReplayControlActions
}) {
  if (!replay.active) return null
  const count = replay.frameRevisions.length
  const pending = replay.preparing || replay.restarting
  return (
    <Card role="region" aria-label="Replay controls">
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <p role="status" className="font-medium tabular-nums">
            {count
              ? `Replay · R${replay.fromRevision}–R${replay.toRevision} · Frame ${formatCount(replay.frameIndex + 1)} of ${formatCount(count)} · ${Math.round(((replay.frameIndex + 1) / count) * 100)}%`
              : `Replay frozen through R${replay.toRevision}`}
          </p>
          {replay.pendingCount > 0 && (
            <Badge variant="secondary" role="status">
              {formatCount(replay.pendingCount)}{" "}
              {replay.pendingCount === 1 ? "update" : "updates"} pending
            </Badge>
          )}
          {pending && (
            <span role="status" className="text-muted-foreground">
              {replay.restarting
                ? "Loading latest Replay…"
                : "Preparing Replay…"}
            </span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="icon"
            aria-label="Previous Replay frame"
            disabled={pending || !count || replay.frameIndex === 0}
            onClick={actions.previous}
          >
            <ChevronLeft aria-hidden="true" />
          </Button>
          <Button
            variant="outline"
            size="default"
            disabled={pending || !count}
            onClick={actions.togglePlayback}
          >
            {replay.playing ? (
              <Pause data-icon="inline-start" aria-hidden="true" />
            ) : (
              <Play data-icon="inline-start" aria-hidden="true" />
            )}
            {replay.playing ? "Pause Replay" : "Play Replay"}
          </Button>
          <Button
            variant="outline"
            size="icon"
            aria-label="Next Replay frame"
            disabled={pending || !count || replay.frameIndex === count - 1}
            onClick={actions.next}
          >
            <ChevronRight aria-hidden="true" />
          </Button>
          <div className="flex min-w-0 items-center gap-2">
            <Label htmlFor="replay-frame">Frame</Label>
            <Select
              disabled={pending || !count}
              value={count ? String(replay.frameIndex) : ""}
              onValueChange={(value) => actions.selectFrame(Number(value))}
            >
              <SelectTrigger
                id="replay-frame"
                aria-label="Go to Replay frame"
                size="default"
              >
                <SelectValue placeholder="Unavailable" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {replay.frameRevisions.map((revision, index) => (
                    <SelectItem key={revision} value={String(index)}>
                      R{revision}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div className="flex min-w-0 items-center gap-2">
            <Label htmlFor="replay-speed">Speed</Label>
            <Select
              disabled={pending}
              value={String(replay.stepMs)}
              onValueChange={(value) => actions.setStepMs(Number(value))}
            >
              <SelectTrigger id="replay-speed" size="default">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="700">Fast</SelectItem>
                  <SelectItem value="1100">Normal</SelectItem>
                  <SelectItem value="1600">Slow</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <Button
            variant="outline"
            size="default"
            disabled={pending}
            onClick={() => void actions.restart()}
          >
            <RotateCcw data-icon="inline-start" aria-hidden="true" /> Restart
            with Latest
          </Button>
          <Button
            variant="outline"
            size="default"
            onClick={() => void actions.exit()}
          >
            <X data-icon="inline-start" aria-hidden="true" /> Exit Replay
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Historical Blackboard is read-only. Exit or restart to include current
          data.
        </p>
      </CardContent>
    </Card>
  )
}
