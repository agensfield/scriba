import ansis from 'ansis'
import type { BenchmarkCommandResult, DatasetSummary } from '../bench/ccusage.ts'
import type { DoctorPayload, DoctorState } from '../doctor/check.ts'
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
	machine?: {
		platform: string
		arch: string
		node: string
		bun: string | null
	}
	tools?: Record<string, string>
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
		lines.push(providerHeader(provider.displayName))
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

export function renderDoctor(payload: DoctorPayload): string {
	const lines = [
		header('Scriba Doctor'),
		`${stateLabel(payload.state)} ${muted(`generated ${payload.generatedAt}`)}`,
		'',
		providerHeader('Cache'),
		`  ${label('State')} ${stateLabel(payload.cache.state)}`,
		`  ${label('Path')} ${payload.cache.databasePath}`,
		`  ${label('Size')} ${formatBytes(payload.cache.sizeBytes)}`,
		`  ${label('Schema')} ${value(String(payload.cache.schemaVersion))}`,
		`  ${label('WAL')} ${payload.cache.walEnabled ? badge('enabled') : ansis.yellowBright.bold('disabled')}`,
		`  ${label('Snapshot age')} ${payload.cache.latestSnapshotAgeMs == null ? ansis.yellowBright.bold('none') : value(duration(payload.cache.latestSnapshotAgeMs))}`,
		'',
	]
	for (const provider of payload.providers) {
		lines.push(
			providerHeader(provider.displayName),
			`  ${label('State')} ${stateLabel(provider.state)}`,
		)
		for (const path of provider.localPaths) {
			lines.push(
				`  ${label('Source')} ${path.exists ? badge('found') : ansis.yellowBright.bold('missing')} ${path.path}`,
			)
		}
		const foundAuth = provider.auth.paths.filter((path) => path.exists)
		lines.push(
			`  ${label('Auth')} ${foundAuth.length > 0 ? badge('found') : ansis.yellowBright.bold('missing')} ${foundAuth.length > 0 ? foundAuth.map((path) => path.path).join(', ') : provider.auth.hint}`,
		)
		lines.push(
			`  ${label('Remote')} ${
				provider.remote.state === 'skipped'
					? ansis.blueBright('skipped')
					: provider.remote.state === 'ok'
						? badge('ok')
						: ansis.yellowBright.bold(provider.remote.error ?? provider.remote.state)
			}`,
		)
		lines.push('')
	}
	return lines.join('\n').trimEnd()
}

function renderMetricLine(line: MetricLine): string {
	if (line.type === 'text') {
		return `${label(line.label)} ${value(line.value)}`
	}
	if (line.type === 'amount') {
		return `${label(line.label)} ${value(formatMetricValue(line.value, line.format))}`
	}
	if (line.type === 'badge') {
		return `${label(line.label)} ${badge(line.text)}`
	}
	const percent = line.limit === 0 ? 0 : Math.round((line.used / line.limit) * 100)
	const details = progressDetails(line)
	return `${label(line.label)} ${bar(percent)} ${percentValue(percent)} ${muted(`used${details.length > 0 ? ` · ${details}` : ''}`)}`
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
	return [
		formatRow(headers.map((heading) => ansis.cyanBright.bold(heading))),
		...rows.map(formatRow),
	].join('\n')
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
	return ansis.cyanBright.bold(value)
}

function muted(value: string): string {
	return ansis.blueBright(value)
}

function label(value: string): string {
	return ansis.cyan(value)
}

function value(text: string): string {
	return ansis.whiteBright.bold(text)
}

function badge(text: string): string {
	return ansis.greenBright.bold(text)
}

function providerHeader(value: string): string {
	return ansis.whiteBright.bold(value)
}

function providerLabel(value: string): string {
	return value === 'claude'
		? ansis.magenta('Claude')
		: value === 'codex'
			? ansis.green('Codex')
			: value
}

function stateLabel(state: DoctorState): string {
	return state === 'ok'
		? ansis.greenBright.bold('ok')
		: state === 'degraded'
			? ansis.yellowBright.bold('degraded')
			: ansis.redBright.bold('broken')
}

function duration(ms: number): string {
	const seconds = Math.round(ms / 1000)
	if (seconds < 60) {
		return `${seconds}s`
	}
	const minutes = Math.round(seconds / 60)
	if (minutes < 60) {
		return `${minutes}m`
	}
	const hours = Math.round(minutes / 60)
	if (hours < 24) {
		return `${hours}h`
	}
	const days = Math.floor(hours / 24)
	const remainderHours = hours % 24
	return remainderHours === 0 ? `${days}d` : `${days}d ${remainderHours}h`
}

function bar(percent: number): string {
	const clamped = Math.max(0, Math.min(percent, 100))
	const filled = Math.round(clamped / 5)
	const color =
		clamped >= 90 ? ansis.redBright : clamped >= 70 ? ansis.yellowBright : ansis.greenBright
	return `${color('▰'.repeat(filled))}${'▱'.repeat(20 - filled)}`
}

function percentValue(percent: number): string {
	const color =
		percent >= 90 ? ansis.redBright : percent >= 70 ? ansis.yellowBright : ansis.whiteBright
	return color.bold(`${percent}%`)
}

function progressDetails(line: Extract<MetricLine, { type: 'progress' }>): string {
	const parts = []
	if (line.format.kind !== 'percent') {
		parts.push(
			`${formatMetricValue(line.used, line.format)} of ${formatMetricValue(line.limit, line.format)}`,
		)
	}
	if (line.resetsAt != null) {
		parts.push(resetLabel(line.resetsAt))
	}
	return parts.join(' · ')
}

function resetLabel(resetsAt: string): string {
	const resetMs = new Date(resetsAt).getTime()
	if (!Number.isFinite(resetMs)) {
		return `resets ${resetsAt}`
	}
	const deltaMs = resetMs - Date.now()
	const absolute = resetTimeLabel(new Date(resetMs))
	if (deltaMs <= 0) {
		return `reset due ${absolute}`
	}
	return `resets in ${duration(deltaMs)} (${absolute})`
}

function resetTimeLabel(date: Date): string {
	const parts = new Intl.DateTimeFormat(undefined, {
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
		hourCycle: 'h23',
	}).formatToParts(date)
	const month = parts.find((part) => part.type === 'month')?.value ?? ''
	const day = parts.find((part) => part.type === 'day')?.value ?? ''
	const hour = parts.find((part) => part.type === 'hour')?.value ?? ''
	const minute = parts.find((part) => part.type === 'minute')?.value ?? ''
	return `${month} ${day} ${hour}:${minute}`.trim()
}

function strip(value: string): string {
	return ansis.strip(value)
}
