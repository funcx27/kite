import { useMemo } from 'react'
import {
  ColumnDef,
  flexRender,
  getCoreRowModel,
  Header,
  useReactTable,
} from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

interface ActionTableProps<T> {
  data: T[]
  columns: ColumnDef<T>[]
  actions: Action<T>[]
  /** 操作列内、菜单按钮之前渲染的内容（如状态徽标） */
  actionCellPrefix?: (item: T) => React.ReactNode
}

export interface Action<T> {
  label: string | React.ReactNode
  dynamicLabel?: (item: T) => string | React.ReactNode
  onClick: (item: T) => void
  shouldDisable?: (item: T) => boolean
}

function renderHeaderCell<T>(header: Header<T, unknown>) {
  return header.isPlaceholder
    ? null
    : flexRender(header.column.columnDef.header, header.getContext())
}

export function ActionTable<T>({
  data,
  columns,
  actions,
  actionCellPrefix,
}: ActionTableProps<T>) {
  const { t } = useTranslation()
  const tableColumns = useMemo(() => {
    // 无 actions 但有前置内容（如状态徽标/直接操作按钮）时仍渲染操作列
    if (actions.length > 0 || actionCellPrefix) {
      const actionColumn: ColumnDef<T> = {
        id: 'actions',
        header: t('common.fields.actions'),
        cell: ({ row }) => (
          <div
            className={
              actionCellPrefix
                ? 'flex items-center justify-end gap-2'
                : 'text-right'
            }
          >
            {actionCellPrefix?.(row.original)}
            {actions.length > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={t('common.fields.actions', 'Actions')}
                  >
                    •••
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  {actions.map((action, index) => (
                    <DropdownMenuItem
                      key={index}
                      disabled={action.shouldDisable?.(row.original)}
                      onClick={() => action.onClick(row.original)}
                      className="gap-2"
                    >
                      {action.dynamicLabel
                        ? action.dynamicLabel(row.original)
                        : action.label}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        ),
      }
      return [...columns, actionColumn]
    }
    return columns
  }, [actions, columns, actionCellPrefix, t])

  const table = useReactTable<T>({
    data,
    columns: tableColumns,
    getCoreRowModel: getCoreRowModel(),
  })

  return (
    <div className="rounded-md border">
      <Table>
        <TableHeader className="bg-muted sticky top-0 z-10">
          {table.getHeaderGroups().map((headerGroup) => (
            <TableRow key={headerGroup.id}>
              {headerGroup.headers.map((header) => (
                <TableHead
                  key={header.id}
                  className={
                    header.id === 'actions'
                      ? actionCellPrefix
                        ? 'text-right'
                        : 'w-14 text-right'
                      : ''
                  }
                >
                  {renderHeaderCell(header)}
                </TableHead>
              ))}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {table.getRowModel().rows.map((row) => (
            <TableRow
              key={row.id}
              data-state={row.getIsSelected() && 'selected'}
            >
              {row.getVisibleCells().map((cell) => (
                <TableCell
                  key={cell.id}
                  className={
                    cell.column.id === 'actions'
                      ? actionCellPrefix
                        ? 'text-right'
                        : 'w-14 text-right'
                      : ''
                  }
                >
                  {cell.column.columnDef.cell
                    ? flexRender(cell.column.columnDef.cell, cell.getContext())
                    : String(cell.getValue() || '-')}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
