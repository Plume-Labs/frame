import { ClusterNode } from '@/lib/types'
import { NodeCard } from './NodeCard'
import { useIsMobile } from '@/hooks/use-mobile'
import { cn } from '@/lib/utils'

interface NodeGridProps {
  nodes: ClusterNode[]
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
}

export function NodeGrid({ nodes, selectedNode, onSelectNode }: NodeGridProps) {
  const isMobile = useIsMobile()
  
  return (
    <div className={cn(
      'grid gap-4',
      isMobile ? 'grid-cols-4' : 'grid-cols-8'
    )}>
      {nodes.map((node) => (
        <NodeCard
          key={node.id}
          node={node}
          onClick={() => onSelectNode(node)}
          isSelected={selectedNode?.id === node.id}
        />
      ))}
    </div>
  )
}
