import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ResourceForecast } from '@/lib/types'
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend, Area, ComposedChart } from 'recharts'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

interface ForecastChartProps {
  forecast: ResourceForecast
}

export function ForecastChart({ forecast }: ForecastChartProps) {
  const formatDate = (timestamp: number) => {
    const date = new Date(timestamp)
    return `${date.getMonth() + 1}/${date.getDate()}`
  }

  const cpuData = forecast.cpu.map(point => ({
    date: formatDate(point.timestamp),
    predicted: point.predicted,
    lower: point.confidence.lower,
    upper: point.confidence.upper
  }))

  const memoryData = forecast.memory.map(point => ({
    date: formatDate(point.timestamp),
    predicted: point.predicted,
    lower: point.confidence.lower,
    upper: point.confidence.upper
  }))

  const storageData = forecast.storage.map(point => ({
    date: formatDate(point.timestamp),
    predicted: point.predicted,
    lower: point.confidence.lower,
    upper: point.confidence.upper
  }))

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono">Resource Forecast</CardTitle>
        <CardDescription>Predicted resource utilization trends</CardDescription>
      </CardHeader>
      <CardContent>
        <Tabs defaultValue="cpu" className="w-full">
          <TabsList className="grid w-full grid-cols-3 mb-4">
            <TabsTrigger value="cpu" className="font-mono text-xs">CPU</TabsTrigger>
            <TabsTrigger value="memory" className="font-mono text-xs">Memory</TabsTrigger>
            <TabsTrigger value="storage" className="font-mono text-xs">Storage</TabsTrigger>
          </TabsList>

          <TabsContent value="cpu">
            {cpuData.length > 0 ? (
              <ResponsiveContainer width="100%" height={300}>
                <ComposedChart data={cpuData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="oklch(0.35 0.02 240)" />
                  <XAxis 
                    dataKey="date" 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
                  />
                  <YAxis 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
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
                    dataKey="upper"
                    stroke="none"
                    fill="oklch(0.75 0.15 195 / 0.2)"
                    name="Upper Bound"
                  />
                  <Area
                    type="monotone"
                    dataKey="lower"
                    stroke="none"
                    fill="oklch(0.25 0.01 240)"
                    name="Lower Bound"
                  />
                  <Line
                    type="monotone"
                    dataKey="predicted"
                    stroke="oklch(0.75 0.15 195)"
                    strokeWidth={2}
                    dot={{ fill: 'oklch(0.75 0.15 195)', r: 3 }}
                    name="Predicted Usage %"
                  />
                </ComposedChart>
              </ResponsiveContainer>
            ) : (
              <div className="h-[300px] flex items-center justify-center text-muted-foreground">
                Insufficient data for forecast
              </div>
            )}
          </TabsContent>

          <TabsContent value="memory">
            {memoryData.length > 0 ? (
              <ResponsiveContainer width="100%" height={300}>
                <ComposedChart data={memoryData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="oklch(0.35 0.02 240)" />
                  <XAxis 
                    dataKey="date" 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
                  />
                  <YAxis 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
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
                    dataKey="upper"
                    stroke="none"
                    fill="oklch(0.85 0.20 145 / 0.2)"
                    name="Upper Bound"
                  />
                  <Area
                    type="monotone"
                    dataKey="lower"
                    stroke="none"
                    fill="oklch(0.25 0.01 240)"
                    name="Lower Bound"
                  />
                  <Line
                    type="monotone"
                    dataKey="predicted"
                    stroke="oklch(0.85 0.20 145)"
                    strokeWidth={2}
                    dot={{ fill: 'oklch(0.85 0.20 145)', r: 3 }}
                    name="Predicted Usage %"
                  />
                </ComposedChart>
              </ResponsiveContainer>
            ) : (
              <div className="h-[300px] flex items-center justify-center text-muted-foreground">
                Insufficient data for forecast
              </div>
            )}
          </TabsContent>

          <TabsContent value="storage">
            {storageData.length > 0 ? (
              <ResponsiveContainer width="100%" height={300}>
                <ComposedChart data={storageData}>
                  <CartesianGrid strokeDasharray="3 3" stroke="oklch(0.35 0.02 240)" />
                  <XAxis 
                    dataKey="date" 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
                  />
                  <YAxis 
                    stroke="oklch(0.60 0.03 240)" 
                    style={{ fontSize: '12px', fontFamily: 'JetBrains Mono' }}
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
                    dataKey="upper"
                    stroke="none"
                    fill="oklch(0.75 0.18 75 / 0.2)"
                    name="Upper Bound"
                  />
                  <Area
                    type="monotone"
                    dataKey="lower"
                    stroke="none"
                    fill="oklch(0.25 0.01 240)"
                    name="Lower Bound"
                  />
                  <Line
                    type="monotone"
                    dataKey="predicted"
                    stroke="oklch(0.75 0.18 75)"
                    strokeWidth={2}
                    dot={{ fill: 'oklch(0.75 0.18 75)', r: 3 }}
                    name="Predicted Usage %"
                  />
                </ComposedChart>
              </ResponsiveContainer>
            ) : (
              <div className="h-[300px] flex items-center justify-center text-muted-foreground">
                Insufficient data for forecast
              </div>
            )}
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
