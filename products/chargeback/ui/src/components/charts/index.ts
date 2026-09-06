/**
 * Chart library for the chargeback UI (#6867, DESIGN.md §5).
 *
 * Dependency-free SVG: the app ships react, react-dom and react-router-dom
 * and nothing else. Every chart shares the palette, the tooltip, the legend
 * and the axis helpers here, and every chart renders <EmptyChart/> — words,
 * not a zero — when there is nothing to draw.
 */
import './charts.css'

export { PALETTE, OTHER_COLOR, OTHER_KEY, FORECAST_COLOR, SURFACE, colorFor, colorForKey } from './palette'
export { niceTicks, niceMax, niceStep, linearTicks, xLabelEvery, shortBucket, fitLabel } from './scale'
export { stackSeries, topN, seriesFromExplore, seriesFromDaily, forecastTail } from './stack'
export type { Series, Stacked, ForecastTail, ChartData } from './stack'
export { useTooltip, ChartTooltip, TipRows } from './tooltip'
export type { TooltipApi, TooltipState, TipRow } from './tooltip'
export { useWidth } from './measure'
export { Legend } from './Legend'
export type { LegendItem } from './Legend'
export { EmptyChart } from './EmptyChart'
export { StackedBars } from './StackedBars'
export type { StackedBarsProps } from './StackedBars'
export { LineChart } from './LineChart'
export type { LineChartProps } from './LineChart'
export { Donut, donutLayout, arcPath } from './Donut'
export type { DonutProps, DonutSlice, DonutArc, DonutLayout, DonutLegendRow } from './Donut'
export { RankedBars, DeltaChip } from './RankedBars'
export type { RankedBarsProps, RankedRow } from './RankedBars'
export { Sparkline } from './Sparkline'
export type { SparklineProps } from './Sparkline'
export { Waterfall, waterfallLayout } from './Waterfall'
export type { WaterfallProps, WaterfallStep, WaterfallBar, WaterfallLayoutResult } from './Waterfall'
export { ProgressBar, progressState } from './ProgressBar'
export type { ProgressBarProps, ProgressMarker, ProgressState } from './ProgressBar'
