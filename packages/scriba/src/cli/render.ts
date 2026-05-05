import ansis from 'ansis'
import type { BenchmarkCommandResult, DatasetSummary } from '../bench/ccusage.ts'
import type { ScannerStats } from '../local/types.ts'
import type { MetricFormat, MetricLine, StatusSnapshot } from '../schema/model.ts'
import type { TelegramAlert } from '../telegram/alerts.ts'

type ReportPayload = {
	providerId: string
	stats: ScannerStats
	rows: Record<string, unknown>[]
}

type BenchmarkPayload = {
	generatedAt: string
	execute: boolean
	timeoutMs: number
	datasets: DatasetSummary[]
	results: BenchmarkCommandResult[]
}

type TelegramPayload = {
	generatedAt: string
	enabled: boolean
	alerts: TelegramAlert[]
	sent: number
}

export function renderStatus(snapshot: StatusSnapshot): string {
	const lines = [header('Scriba Status'), muted(`generated ${snapshot.generatedAt}`), '']
	for (const provider of snapshot.providers) {
		lines.push(ansis.bold(provider.displayName))
		for (const line of provider.lines) {
			lines.push(`  ${renderMetricLine(line)}`)
		}
		const errors = provider.provenance
			.map((source) => source.error)
			.filter((error): error is string => error != null)
		for (const error of errors) {
			lines.push(`  ${ansis.red('error')} ${error}`)
		}
		lines.push('')
	}
	return lines.join('\n').trimEnd()
}

export function renderReport(title: string, payload: ReportPayload): string {
	const rows = payload.rows.slice(0, 30)
	const lines = [
		header(title),
		muted(
			`${payload.stats.events.toLocaleString()} events from ${payload.stats.files.toLocaleString()} files (${formatBytes(payload.stats.bytes)})`,
		),
		'',
	]
	if (rows.length === 0) {
		lines.push(muted('no usage rows found'))
		return lines.join('\n')
	}

	const key =
		firstPresentKey(rows, ['date', 'week', 'month', 'sessionId', 'id', 'startTime']) ?? 'row'
	const tableRows = rows.map((row) => [
		cell(row[key]),
		tokens(row.totalTokens),
		tokens(row.inputTokens),
		tokens(row.outputTokens),
		tokens(row.cacheReadTokens ?? row.cachedInputTokens),
		cost(row.costUSD),
	])
	lines.push(table(['bucket', 'total', 'input', 'output', 'cache', 'cost'], tableRows))
	if (payload.rows.length > rows.length) {
		lines.push(
			muted(`showing ${rows.length} of ${payload.rows.length} rows; use --json for all rows`),
		)
	}
	return lines.join('\n')
}

export function renderBenchmark(payload: BenchmarkPayload): string {
	const lines = [
		header('ccusage Benchmark'),
		payload.execute
			? muted(`executed with ${payload.timeoutMs}ms timeout`)
			: muted('safe mode: commands were not executed'),
		'',
		ansis.bold('Datasets'),
	]
	for (const dataset of payload.datasets) {
		lines.push(
			`  ${providerLabel(dataset.providerId)} ${dataset.files.toLocaleString()} files, ${formatBytes(dataset.bytes)}`,
		)
		for (const missing of dataset.missingDirectories) {
			lines.push(`    ${ansis.yellow('missing')} ${missing}`)
		}
	}
	lines.push('', ansis.bold('Commands'))
	for (const result of payload.results) {
		const state = result.executed
			? result.ok
				? ansis.green('ok')
				: result.timedOut
					? ansis.yellow('timeout')
					: ansis.red('failed')
			: ansis.gray('planned')
		const duration = result.durationMs == null ? '' : ` ${result.durationMs}ms`
		lines.push(`  ${state} ${result.command} ${result.args.join(' ')}${duration}`)
		if (result.error != null) {
			lines.push(`    ${ansis.red(result.error)}`)
		}
	}
	return lines.join('\n')
}

export function renderTelegram(payload: TelegramPayload): string {
	const lines = [
		header('Telegram Alerts'),
		muted(`${payload.enabled ? 'enabled' : 'disabled'}; ${payload.sent} sent`),
		'',
	]
	if (payload.alerts.length === 0) {
		lines.push(muted('no alerts'))
		return lines.join('\n')
	}
	for (const alert of payload.alerts) {
		const color = alert.severity === 'error' ? ansis.red : ansis.yellow
		lines.push(`  ${color(alert.severity)} ${alert.providerId} ${alert.message}`)
	}
	return lines.join('\n')
}

function renderMetricLine(line: MetricLine): string {
	if (line.type === 'text') {
		return `${muted(line.label)} ${ansis.bold(line.value)}`
	}
	if (line.type === 'amount') {
		return `${muted(line.label)} ${ansis.bold(formatMetricValue(line.value, line.format))}`
	}
	if (line.type === 'badge') {
		return `${muted(line.label)} ${ansis.cyan(line.text)}`
	}
	const percent = line.limit === 0 ? 0 : Math.round((line.used / line.limit) * 100)
	return `${muted(line.label)} ${bar(percent)} ${ansis.bold(`${percent}%`)} ${muted(`(${formatMetricValue(line.used, line.format)} / ${formatMetricValue(line.limit, line.format)})`)}`
}

function formatMetricValue(value: number, format: MetricFormat): string {
	if (format.kind === 'percent') {
		return `${Math.round(value)}%`
	}
	if (format.kind === 'dollars') {
		return `$${value.toFixed(value < 10 ? 2 : 0)}`
	}
	return `${Intl.NumberFormat('en-US', { maximumFractionDigits: 2 }).format(value)} ${format.suffix}`
}

function table(headers: string[], rows: string[][]): string {
	const widths = headers.map((heading, index) =>
		Math.max(heading.length, ...rows.map((row) => strip(row[index] ?? '').length)),
	)
	const formatRow = (row: string[]) =>
		row.map((value, index) => value.padStart(widths[index] ?? 0)).join('  ')
	return [formatRow(headers.map((heading) => ansis.gray(heading))), ...rows.map(formatRow)].join(
		'\n',
	)
}

function firstPresentKey(rows: Record<string, unknown>[], keys: string[]): string | undefined {
	return keys.find((key) => rows.some((row) => row[key] != null))
}

function cell(value: unknown): string {
	if (typeof value === 'string') {
		return value.length > 28 ? `${value.slice(0, 25)}...` : value
	}
	return value == null ? '-' : String(value)
}

function tokens(value: unknown): string {
	return typeof value === 'number' ? compactNumber(value) : '-'
}

function cost(value: unknown): string {
	return typeof value === 'number' ? `$${value.toFixed(4)}` : '-'
}

function compactNumber(value: number): string {
	return Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}

function formatBytes(value: number): string {
	return Intl.NumberFormat('en-US', {
		style: 'unit',
		unit: 'byte',
		notation: 'compact',
		maximumFractionDigits: 1,
	}).format(value)
}

function header(value: string): string {
	return ansis.bold.cyan(value)
}

function muted(value: string): string {
	return ansis.gray(value)
}

function providerLabel(value: string): string {
	return value === 'claude'
		? ansis.magenta('Claude')
		: value === 'codex'
			? ansis.green('Codex')
			: value
}

function bar(percent: number): string {
	const clamped = Math.max(0, Math.min(percent, 100))
	const filled = Math.round(clamped / 10)
	return `${ansis.green('█'.repeat(filled))}${ansis.gray('░'.repeat(10 - filled))}`
}

function strip(value: string): string {
	return ansis.strip(value)
}
