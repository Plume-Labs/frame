import { useState, useMemo } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ResourceDataPoint, TimeframeOption, TrendStatistics } from '@/lib/types'
import {
  analyzeHistoricalTrends,
  getTimeframeLabel,
} from '@/lib/trends'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
  Area,
  ComposedChart
} from 'recharts'
import { TrendUp, TrendDown, Minus, ChartLine } from '@phosphor-icons/react'
import { Badge } from '@/components/ui/badge'

interface HistoricalTrendsAnalysisProps {
  historicalData: ResourceDataPoint[]
}

export function HistoricalTrendsAnalysis({ historicalData }: HistoricalTrendsAnalysisProps) {
  const [selectedTimeframe, setSelectedTimeframe] = useState<TimeframeOption>('24h')

  const trendData = useMemo(() => {
    return analyzeHistoricalTrends(historicalData, selectedTimeframe)
  }, [historicalData, selectedTimeframe])

  const formatDate = (timestamp: number) => {
    const date = new Date(timestamp)
    
    if (selectedTimeframe === '1h' || selectedTimeframe === '6h') {
      return `${date.getHours().toString().padStart(2, '0')}:${date.getMinutes().toString().padStart(2, '0')}`
    } else if (selectedTimeframe === '24h') {
      return `${date.getHours().toString().padStart(2, '0')}:00`
    } else {
      return `${date.getMonth() + 1}/${date.getDate()}`
    }
  }

  const chartData = useMemo(() => {
    return trendData.data.map(point => ({
      time: formatDate(point.timestamp),
      cpu: point.cpu.toFixed(1),
      memory: point.memory.toFixed(1),
      storage: point.storage.toFixed(1),
      network: point.network.toFixed(1)
    }))
  }, [trendData.data, selectedTimeframe])

  const renderTrendIcon = (trend: 'increasing' | 'decreasing' | 'stable') => {
    switch (trend) {
      case 'increasing':
        return <TrendUp className="text-warning" weight="bold" />
      case 'decreasing':
        return <TrendDown className="text-accent" weight="bold" />
      case 'stable':
        return <Minus className="text-muted-foreground" weight="bold" />
    }
  }

  const getTrendColor = (trend: 'increasing' | 'decreasing' | 'stable') => {
    switch (trend) {
      case 'increasing':
        return 'text-warning'
      case 'decreasing':
        return 'text-accent'
      case 'stable':
        return 'text-muted-foreground'
    }
  }

  const formatTimestamp = (timestamp: number) => {
    const date = new Date(timestamp)
    return date.toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    })
  }

  const renderStatisticsCard = (stats: TrendStatistics) => {
    const resourceColors = {
      cpu: 'oklch(0.75 0.15 195)',
      memory: 'oklch(0.85 0.20 145)',
      storage: 'oklch(0.75 0.18 75)',
      network: 'oklch(0.70 0.15 270)'
    }

    return (
      <Card key={stats.resource} className="border-border/50">
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <CardTitle className="font-mono text-lg uppercase">
              {stats.resource}
            </CardTitle>
            <div className="flex items-center gap-2">
              {renderTrendIcon(stats.trend)}
              <span className={`font-mono text-sm font-semibold ${getTrendColor(stats.trend)}`}>
                {stats.trend === 'stable' ? 'Stable' : `${stats.trendPercentage.toFixed(1)}%`}
              </span>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Current</p>
              <p className="text-2xl font-mono font-bold" style={{ color: resourceColors[stats.resource] }}>
                {stats.current.toFixed(1)}%
              </p>
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Average</p>
              <p className="text-2xl font-mono font-bold text-foreground">
                {stats.average.toFixed(1)}%
              </p>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-4 pt-2 border-t border-border/50">
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Min</p>
              <p className="text-lg font-mono text-accent">
                {stats.min.toFixed(1)}%
              </p>
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Max</p>
              <p className="text-lg font-mono text-warning">
                {stats.max.toFixed(1)}%
              </p>
            </div>
          </div>

          <div className="space-y-2 pt-2 border-t border-border/50">
            <div className="flex items-center justify-between text-xs">
              <span className="text-muted-foreground font-mono">Median</span>
              <span className="font-mono text-foreground">{stats.median.toFixed(1)}%</span>
            </div>
            <div className="flex items-center justify-between text-xs">
              <span className="text-muted-foreground font-mono">Std Dev</span>
              <span className="font-mono text-foreground">{stats.stdDeviation.toFixed(2)}</span>
            </div>
          </div>

          <div className="space-y-2 pt-2 border-t border-border/50">
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Peak</p>
              <p className="text-sm font-mono text-warning">
                {stats.peakValue.toFixed(1)}% at {formatTimestamp(stats.peakTime)}
              </p>
            </div>
            <div className="space-y-1">
              <p className="text-xs text-muted-foreground font-mono">Low</p>
              <p className="text-sm font-mono text-accent">
                {stats.lowValue.toFixed(1)}% at {formatTimestamp(stats.lowTime)}
              </p>
            </div>
          </div>
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <div className="space-y-1">
              <CardTitle className="font-mono flex items-center gap-2">
                <ChartLine weight="bold" className="text-primary" />
                Historical Trend Analysis
              </CardTitle>
              <CardDescription>
                Resource utilization trends and statistics
              </CardDescription>
            </div>
            <Select
              value={selectedTimeframe}
              onValueChange={(value) => setSelectedTimeframe(value as TimeframeOption)}
            >
              <SelectTrigger className="w-[180px] font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="1h" className="font-mono">Last Hour</SelectItem>
                <SelectItem value="6h" className="font-mono">Last 6 Hours</SelectItem>
                <SelectItem value="24h" className="font-mono">Last 24 Hours</SelectItem>
                <SelectItem value="7d" className="font-mono">Last 7 Days</SelectItem>
                <SelectItem value="30d" className="font-mono">Last 30 Days</SelectItem>
                <SelectItem value="all" className="font-mono">All Time</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </CardHeader>
        <CardContent>
          {trendData.data.length > 0 ? (
            <>
              <div className="mb-6">
                <Tabs defaultValue="combined" className="w-full">
                  <TabsList className="grid w-full grid-cols-5 mb-4">
                    <TabsTrigger value="combined" className="font-mono text-xs">All</TabsTrigger>
                    <TabsTrigger value="cpu" className="font-mono text-xs">CPU</TabsTrigger>
                    <TabsTrigger value="memory" className="font-mono text-xs">Memory</TabsTrigger>
                    <TabsTrigger value="storage" className="font-mono text-xs">Storage</TabsTrigger>
                    <TabsTrigger value="network" className="font-mono text-xs">Network</TabsTrigger>
                  </TabsList>

                  <TabsContent value="combined">
                    <ResponsiveContainer width="100%" height={350}>
                      <LineChart data={chartData}>
                        <CartesianGrid strokeDasharray="3 3" stroke="oklch(0.35 0.02 240)" />
                        <XAxis
                          dataKey="time"
                          stroke="oklch(0.60 0.03 240)"
                          style={{ fontSize: '11px', fontFamily: 'JetBrains Mono' }}
                        />
                        <YAxis
                          stroke="oklch(0.60 0.03 240)"
                          style={{ fontSize: '11px', fontFamily: 'JetBrains Mono' }}
                          domain={[0, 100]}
                        />
                        <Tooltip
                          contentStyle={{
                            backgroundColor: 'oklch(0.20 0.015 240)',
                            border: '1px solid oklch(0.35 0.02 240)',
                            borderRadius: '6px',
                            fontFamily: 'JetBrains Mono',
                            fontSize: '12px'
                          }}
                        />
                        <Legend />
                        <Line
                          type="monotone"
                          dataKey="cpu"
                          stroke="oklch(0.75 0.15 195)"
                          strokeWidth={2}
                          dot={false}
                          name="CPU %"
                        />
                        <Line
                          type="monotone"
                          dataKey="memory"
                          stroke="oklch(0.85 0.20 145)"
                          strokeWidth={2}
                          dot={false}
                          name="Memory %"
                        />
                        <Line
                          type="monotone"
                          dataKey="storage"
                          stroke="oklch(0.75 0.18 75)"
                          strokeWidth={2}
                          dot={false}
                          name="Storage %"
                        />
                        <Line
                          type="monotone"
                          dataKey="network"
                          stroke="oklch(0.70 0.15 270)"
                          strokeWidth={2}
                          dot={false}
                          name="Network"
                        />
                      </LineChart>
                    </ResponsiveContainer>
                  </TabsContent>

                  {['cpu', 'memory', 'storage', 'network'].map((resource) => {
                    const colors = {
                      cpu: 'oklch(0.75 0.15 195)',
                      memory: 'oklch(0.85 0.20 145)',
                      storage: 'oklch(0.75 0.18 75)',
                      network: 'oklch(0.70 0.15 270)'
                    }

                    return (
                      <TabsContent key={resource} value={resource}>
                        <ResponsiveContainer width="100%" height={350}>
                          <ComposedChart data={chartData}>
                            <CartesianGrid strokeDasharray="3 3" stroke="oklch(0.35 0.02 240)" />
                            <XAxis
                              dataKey="time"
                              stroke="oklch(0.60 0.03 240)"
                              style={{ fontSize: '11px', fontFamily: 'JetBrains Mono' }}
                            />
                            <YAxis
                              stroke="oklch(0.60 0.03 240)"
                              style={{ fontSize: '11px', fontFamily: 'JetBrains Mono' }}
                              domain={[0, 100]}
                            />
                            <Tooltip
                              contentStyle={{
                                backgroundColor: 'oklch(0.20 0.015 240)',
                                border: '1px solid oklch(0.35 0.02 240)',
                                borderRadius: '6px',
                                fontFamily: 'JetBrains Mono',
                                fontSize: '12px'
                              }}
                            />
                            <Legend />
                            <Area
                              type="monotone"
                              dataKey={resource}
                              stroke="none"
                              fill={`${colors[resource as keyof typeof colors]} / 0.15`}
                              name={`${resource.toUpperCase()} Range`}
                            />
                            <Line
                              type="monotone"
                              dataKey={resource}
                              stroke={colors[resource as keyof typeof colors]}
                              strokeWidth={3}
                              dot={{ fill: colors[resource as keyof typeof colors], r: 3 }}
                              name={`${resource.toUpperCase()} %`}
                            />
                          </ComposedChart>
                        </ResponsiveContainer>
                      </TabsContent>
                    )
                  })}
                </Tabs>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                {trendData.statistics.map(renderStatisticsCard)}
              </div>
            </>
          ) : (
            <div className="h-[400px] flex items-center justify-center">
              <div className="text-center space-y-2">
                <ChartLine className="mx-auto text-muted-foreground" size={48} weight="thin" />
                <p className="text-muted-foreground font-mono">
                  No data available for {getTimeframeLabel(selectedTimeframe).toLowerCase()}
                </p>
                <p className="text-xs text-muted-foreground">
                  Historical data is collected every 10 seconds
                </p>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {trendData.data.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">Trend Summary</CardTitle>
            <CardDescription>
              Key observations for {getTimeframeLabel(selectedTimeframe).toLowerCase()}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
              {trendData.statistics.map((stat) => (
                <div
                  key={stat.resource}
                  className="flex items-center justify-between p-3 rounded-lg border border-border/50 bg-card/50"
                >
                  <div className="space-y-1">
                    <p className="text-xs font-mono text-muted-foreground uppercase">
                      {stat.resource}
                    </p>
                    <p className="text-sm font-mono font-semibold text-foreground">
                      {stat.trend === 'stable' ? 'Stable' : stat.trend}
                    </p>
                  </div>
                  <div className="flex flex-col items-end gap-1">
                    {renderTrendIcon(stat.trend)}
                    {stat.trend !== 'stable' && (
                      <Badge variant="outline" className="font-mono text-xs">
                        {stat.trendPercentage.toFixed(1)}%
                      </Badge>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
