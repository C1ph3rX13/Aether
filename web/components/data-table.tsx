"use client"

import { useEffect, useMemo, useState, type ReactNode } from "react"
import {
  columnFilteringFeature,
  globalFilteringFeature,
  rowPaginationFeature,
  rowSortingFeature,
  createColumnHelper,
  createFilteredRowModel,
  createPaginatedRowModel,
  createSortedRowModel,
  tableFeatures,
  useTable,
  FlexRender,
  filterFn_equals,
  sortFn_basic,
  sortFn_text,
} from "@tanstack/react-table"
import {
  IconArrowDown as ArrowDown,
  IconArrowUp as ArrowUp,
  IconArrowsUpDown as ArrowUpDown,
  IconChevronLeft as ChevronLeft,
  IconChevronRight as ChevronRight,
  IconChevronsLeft as ChevronsLeft,
  IconChevronsRight as ChevronsRight,
} from "@tabler/icons-react"
import { useRouter } from "next/navigation"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, FieldLabel, FieldTitle } from "@/components/ui/field"
import { StatusBadge } from "@/components/status-badge"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { CatalogProject } from "@/lib/project-catalog"
import { formatCount, formatCreatedTime } from "@/lib/format"

const features = tableFeatures({
  columnFilteringFeature,
  globalFilteringFeature,
  rowPaginationFeature,
  rowSortingFeature,
  filteredRowModel: createFilteredRowModel(),
  paginatedRowModel: createPaginatedRowModel(),
  sortedRowModel: createSortedRowModel(),
})
const helper = createColumnHelper<typeof features, CatalogProject>()
const statusLabels = {
  active: "Active",
  paused: "Paused",
  completed: "Completed",
}
const executionLabels = {
  retry_exhausted: "Retry exhausted",
  failed_or_interrupted: "Failed or interrupted",
  retrying: "Retrying",
  running: "Running",
  idle: "Idle",
}
const recordLabels: Record<string, string> = {
  status: "Lifecycle",
  execution: "Execution",
  facts: "Facts",
  intents: "Intents",
  createdAt: "Created",
  actions: "Actions",
}

function SortHeader({
  label,
  sort,
  onClick,
}: {
  label: string
  sort: false | "asc" | "desc"
  onClick?: (event: unknown) => void
}) {
  const Icon =
    sort === "asc" ? ArrowUp : sort === "desc" ? ArrowDown : ArrowUpDown
  return (
    <Button
      variant="ghost"
      size="default"
      className="w-full justify-between @min-[40rem]/catalog:w-auto @min-[40rem]/catalog:justify-center"
      onClick={onClick}
      aria-label={`Sort by ${label}`}
    >
      <span>{label}</span>
      <Icon data-icon="inline-end" aria-hidden="true" />
    </Button>
  )
}

const readColumns = helper.columns([
  helper.accessor("title", {
    sortFn: sortFn_text,
    header: ({ column }) => (
      <SortHeader
        label="Project"
        sort={column.getIsSorted()}
        onClick={column.getToggleSortingHandler()}
      />
    ),
    cell: ({ row }) => (
      <div className="flex min-w-0 justify-start @min-[40rem]/catalog:justify-center">
        <Button
          variant="link"
          asChild
          className="h-auto min-h-9 max-w-sm min-w-0 shrink wrap-anywhere whitespace-normal"
        >
          <a href={`/projects/${encodeURIComponent(row.original.id)}`}>
            {row.original.title}
          </a>
        </Button>
      </div>
    ),
  }),
  helper.accessor("status", {
    header: "Lifecycle",
    filterFn: filterFn_equals,
    enableSorting: false,
    enableGlobalFilter: false,
    cell: ({ getValue }) => <StatusBadge value={getValue()} />,
  }),
  helper.display({
    id: "execution",
    header: "Execution",
    cell: ({ row }) => (
      <Badge variant="outline">
        {executionLabels[row.original.execution.status]}
      </Badge>
    ),
  }),
  ...(["facts", "intents"] as const).map((kind) => {
    const label = { facts: "Facts", intents: "Intents", hints: "Hints" }[kind]
    const currentLabel = kind === "intents" ? "open" : "active"
    return helper.accessor((project) => project.blackboard[kind].total, {
      id: kind,
      enableGlobalFilter: false,
      sortFn: sortFn_basic,
      header: ({ column }) => (
        <SortHeader
          label={label}
          sort={column.getIsSorted()}
          onClick={column.getToggleSortingHandler()}
        />
      ),
      cell: ({ row }) => {
        const counts = row.original.blackboard[kind]
        return (
          <span
            className="whitespace-nowrap tabular-nums"
            aria-label={`${label}: ${formatCount(counts.current)} ${currentLabel} of ${formatCount(counts.total)} total`}
          >
            {formatCount(counts.total)}
          </span>
        )
      },
    })
  }),
  helper.accessor((project) => Date.parse(project.createdAt), {
    id: "createdAt",
    enableGlobalFilter: false,
    sortFn: sortFn_basic,
    header: ({ column }) => (
      <SortHeader
        label="Created"
        sort={column.getIsSorted()}
        onClick={column.getToggleSortingHandler()}
      />
    ),
    cell: ({ row }) => (
      <time className="tabular-nums" dateTime={row.original.createdAt}>
        {formatCreatedTime(row.original.createdAt)}
      </time>
    ),
  }),
])

export function DataTable({
  data,
  renderActions,
  renderBulkActions,
  toolbarActions,
}: {
  data: CatalogProject[]
  renderActions: (project: CatalogProject) => ReactNode
  toolbarActions: ReactNode
  renderBulkActions: (
    projects: CatalogProject[],
    clear: () => void
  ) => ReactNode
}) {
  const router = useRouter()
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const selectedProjects = data.filter((project) =>
    selectedIds.includes(project.id)
  )
  const columns = useMemo(
    () =>
      helper.columns([
        helper.display({
          id: "selection",
          header: ({ table }) => {
            const visible = table
              .getFilteredRowModel()
              .rows.map((row) => row.original.id)
            const count = visible.filter((id) =>
              selectedIds.includes(id)
            ).length
            return (
              <FieldLabel className="@min-[40rem]/catalog:mx-auto">
                <Checkbox
                  aria-label="Select all visible Projects"
                  disabled={!visible.length}
                  checked={
                    count === visible.length && count > 0
                      ? true
                      : count > 0
                        ? "indeterminate"
                        : false
                  }
                  onCheckedChange={(checked) =>
                    setSelectedIds((current) =>
                      checked === true
                        ? [...new Set([...current, ...visible])]
                        : current.filter((id) => !visible.includes(id))
                    )
                  }
                />
                <span className="@min-[40rem]/catalog:hidden">
                  Select all visible Projects
                </span>
              </FieldLabel>
            )
          },
          cell: ({ row }) => (
            <Checkbox
              aria-label={`Select ${row.original.title}`}
              checked={selectedIds.includes(row.original.id)}
              onCheckedChange={(checked) =>
                setSelectedIds((current) =>
                  checked === true
                    ? [...new Set([...current, row.original.id])]
                    : current.filter((id) => id !== row.original.id)
                )
              }
            />
          ),
          enableSorting: false,
          enableGlobalFilter: false,
        }),
        ...readColumns,
        helper.display({
          id: "actions",
          header: "Actions",
          cell: ({ row }) => renderActions(row.original),
        }),
      ]),
    [renderActions, selectedIds]
  )
  const table = useTable({
    features,
    data,
    columns,
    getRowId: (project) => project.id,
    autoResetPageIndex: false,
    globalFilterFn: (row, _columnId, value: string) =>
      `${row.original.title} ${row.original.id}`
        .toLocaleLowerCase()
        .includes(value.trim().toLocaleLowerCase()),
    initialState: { pagination: { pageIndex: 0, pageSize: 10 } },
  })
  const pageIndex = table.state.pagination.pageIndex
  const pageCount = Math.max(1, table.getPageCount())
  const matchedCount = table.getFilteredRowModel().rows.length
  const visibleSelected = table
    .getFilteredRowModel()
    .rows.filter((row) => selectedIds.includes(row.original.id)).length
  useEffect(() => {
    if (pageIndex >= pageCount) table.setPageIndex(pageCount - 1)
  }, [table, pageIndex, pageCount])
  const clear = () => {
    table.resetGlobalFilter()
    table.resetColumnFilters()
    table.firstPage()
  }
  return (
    <section
      aria-label="Project Catalog table"
      className="@container/catalog flex min-w-0 flex-col gap-4"
    >
      <div className="flex flex-wrap items-center gap-3">
        <Field className="max-w-sm min-w-48 flex-1">
          <FieldLabel className="sr-only" htmlFor="project-search">
            Search Projects
          </FieldLabel>
          <Input
            id="project-search"
            placeholder="Search Projects…"
            value={table.state.globalFilter ?? ""}
            onChange={(event) => {
              table.setGlobalFilter(event.target.value)
              table.firstPage()
            }}
          />
        </Field>
        <Select
          value={
            (table.getColumn("status")?.getFilterValue() as string) ?? "all"
          }
          onValueChange={(value) => {
            table
              .getColumn("status")
              ?.setFilterValue(value === "all" ? undefined : value)
            table.firstPage()
          }}
        >
          <SelectTrigger aria-label="Lifecycle" id="lifecycle-filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="all">All lifecycles</SelectItem>
              {Object.entries(statusLabels).map(([value, label]) => (
                <SelectItem key={value} value={value}>
                  {label}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <div className="ml-auto flex flex-wrap items-center justify-end gap-2">
          {toolbarActions}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span
          role="status"
          aria-live="polite"
          className="text-sm text-muted-foreground"
        >
          {formatCount(selectedProjects.length)} selected
          {selectedProjects.length > visibleSelected
            ? ` · ${formatCount(selectedProjects.length - visibleSelected)} hidden by filters`
            : ""}
        </span>
        {renderBulkActions(selectedProjects, () => setSelectedIds([]))}
        {selectedProjects.length > 0 && (
          <Button variant="ghost" onClick={() => setSelectedIds([])}>
            Clear selection
          </Button>
        )}
      </div>
      <div className="overflow-hidden rounded-lg border">
        <Table
          aria-label="Projects"
          className="block @min-[40rem]/catalog:table"
        >
          <TableHeader className="block @min-[40rem]/catalog:table-header-group">
            {table.getHeaderGroups().map((group) => (
              <TableRow
                key={group.id}
                className="grid grid-cols-2 gap-1 p-3 @min-[40rem]/catalog:table-row @min-[40rem]/catalog:p-0"
              >
                {group.headers.map((header) => (
                  <TableHead
                    key={header.id}
                    className={cn(
                      "block h-auto px-0 text-center align-middle @min-[40rem]/catalog:table-cell @min-[40rem]/catalog:h-10 @min-[40rem]/catalog:px-2",
                      header.column.id === "selection" &&
                        "col-span-2 p-2 @min-[40rem]/catalog:p-0",
                      recordLabels[header.column.id] &&
                        !header.column.getCanSort() &&
                        "sr-only @min-[40rem]/catalog:not-sr-only"
                    )}
                    aria-sort={
                      header.column.getIsSorted() === "asc"
                        ? "ascending"
                        : header.column.getIsSorted() === "desc"
                          ? "descending"
                          : undefined
                    }
                  >
                    {!header.isPlaceholder && <FlexRender header={header} />}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody className="block @min-[40rem]/catalog:table-row-group">
            {table.getRowModel().rows.map((row) => (
              <TableRow
                key={row.id}
                tabIndex={0}
                aria-label={`Open ${row.original.title}`}
                aria-selected={selectedIds.includes(row.original.id)}
                data-state={
                  selectedIds.includes(row.original.id) ? "selected" : undefined
                }
                className="grid cursor-pointer grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2.5 p-4 @min-[40rem]/catalog:table-row @min-[40rem]/catalog:p-0"
                onClick={(event) => {
                  if (
                    !(event.target as Element).closest(
                      'button,a,input,[role="checkbox"],[role="menuitem"],td:last-child [data-slot="field-label"]'
                    )
                  )
                    router.push(
                      `/projects/${encodeURIComponent(row.original.id)}`
                    )
                }}
                onKeyDown={(event) => {
                  if (
                    event.target === event.currentTarget &&
                    (event.key === "Enter" || event.key === " ")
                  ) {
                    event.preventDefault()
                    router.push(
                      `/projects/${encodeURIComponent(row.original.id)}`
                    )
                  }
                }}
              >
                {row.getAllCells().map((cell) => (
                  <TableCell
                    key={cell.id}
                    className={cn(
                      "flex min-w-0 items-center justify-between gap-4 p-0 whitespace-normal @min-[40rem]/catalog:table-cell @min-[40rem]/catalog:p-2 @min-[40rem]/catalog:text-center @min-[40rem]/catalog:align-middle @min-[40rem]/catalog:whitespace-nowrap",
                      recordLabels[cell.column.id] && "col-span-2",
                      ["title", "execution"].includes(cell.column.id) &&
                        "@min-[40rem]/catalog:whitespace-normal"
                    )}
                  >
                    {recordLabels[cell.column.id] && (
                      <FieldTitle
                        aria-hidden="true"
                        className="@min-[40rem]/catalog:hidden"
                      >
                        {recordLabels[cell.column.id]}
                      </FieldTitle>
                    )}
                    <FlexRender cell={cell} />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {table.getRowModel().rows.length === 0 && (
          <Empty>
            <EmptyHeader>
              <EmptyTitle>
                {data.length === 0 ? "No Projects yet" : "No matching Projects"}
              </EmptyTitle>
              <EmptyDescription>
                {data.length === 0
                  ? "Projects will appear here when they are created."
                  : "Try a different search or lifecycle."}
              </EmptyDescription>
            </EmptyHeader>
            {data.length > 0 && (
              <EmptyContent>
                <Button variant="outline" onClick={clear}>
                  Clear filters
                </Button>
              </EmptyContent>
            )}
          </Empty>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
        <p role="status" className="text-muted-foreground">
          {formatCount(matchedCount)} of {formatCount(data.length)} Projects
        </p>
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <div className="flex items-center gap-3">
            <Label htmlFor="rows-per-page">Rows per page</Label>
            <Select
              value={String(table.state.pagination.pageSize)}
              onValueChange={(value) => {
                table.setPageSize(Number(value))
                table.firstPage()
              }}
            >
              <SelectTrigger id="rows-per-page" size="default">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {[10, 20, 50].map((size) => (
                    <SelectItem key={size} value={String(size)}>
                      {formatCount(size)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <span className="tabular-nums">
            Page {formatCount(Math.min(pageIndex + 1, pageCount))} of{" "}
            {formatCount(pageCount)}
          </span>
          <div className="flex flex-wrap gap-1">
            {[
              {
                label: "Go to first page",
                Icon: ChevronsLeft,
                action: () => table.firstPage(),
                disabled: !table.getCanPreviousPage(),
              },
              {
                label: "Go to previous page",
                Icon: ChevronLeft,
                action: () => table.previousPage(),
                disabled: !table.getCanPreviousPage(),
              },
              {
                label: "Go to next page",
                Icon: ChevronRight,
                action: () => table.nextPage(),
                disabled: !table.getCanNextPage(),
              },
              {
                label: "Go to last page",
                Icon: ChevronsRight,
                action: () => table.lastPage(),
                disabled: !table.getCanNextPage(),
              },
            ].map(({ label, Icon, action, disabled }) => (
              <Button
                key={label}
                variant="outline"
                size="icon"
                aria-label={label}
                onClick={action}
                disabled={disabled}
              >
                <Icon aria-hidden="true" />
              </Button>
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}
