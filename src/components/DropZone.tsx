interface DropZoneProps {
  position: number
  rackId: string
  isHovered: boolean
  isTarget: boolean
  onDragOver: (e: React.DragEvent, position: number) => void
  onDragLeave: () => void
  onDrop: (e: React.DragEvent, position: number) => void
}

export function DropZone({
  position,
  isHovered,
  isTarget,
  onDragOver,
  onDragLeave,
  onDrop
}: DropZoneProps) {
  return (
    <div
      onDragOver={(e) => onDragOver(e, position)}
      onDragLeave={onDragLeave}
      onDrop={(e) => onDrop(e, position)}
      className={`
        border border-dashed transition-all
        ${isHovered || isTarget 
          ? 'border-primary bg-primary/10 border-2' 
          : 'border-border/30 bg-transparent'
        }
      `}
      style={{ height: '20px', minHeight: '20px' }}
    >
      {(isHovered || isTarget) && (
        <div className="h-full flex items-center justify-center">
          <span className="text-[8px] font-mono text-primary">Drop here</span>
        </div>
      )}
    </div>
  )
}
