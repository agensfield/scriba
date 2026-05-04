import type { ScribaConfig } from '../config/schema.ts'
import { scanClaudeLogs } from '../local/claude.ts'
import { scanCodexLogs } from '../local/codex.ts'
import type { ScannerStats } from '../local/types.ts'
import { buildDailyReport } from '../reports/local.ts'
import {
	type MetricLine,
	type ProviderSnapshot,
	SCHEMA_VERSION,
	type StatusSnapshot,
} from '../schema/model.ts'

export type BuildStatusOptions = {
	config: ScribaConfig
	now?: Date
}

export type BuiltStatus = {
	snapshot: StatusSnapshot
	scanStats: Record<'claude' | 'codex', ScannerStats | null>
}

export async function buildStatusSnapshot(options: BuildStatusOptions): Promise<BuiltStatus> {
	const generatedAt = (options.now ?? new Date()).toISOString()
	const providers: ProviderSnapshot[] = []
	const scanStats: BuiltStatus['scanStats'] = { claude: null, codex: null }

	if (options.config.providers.claude.enabled) {
		const scan = await scanClaudeLogs({ paths: optionPaths(options.config.providers.claude.paths) })
		scanStats.claude = scan.stats
		providers.push(providerFromDailyReports('claude', 'Claude', scan.events, generatedAt))
	}

	if (options.config.providers.codex.enabled) {
		const scan = await scanCodexLogs({ paths: optionPaths(options.config.providers.codex.paths) })
		scanStats.codex = scan.stats
		providers.push(providerFromDailyReports('codex', 'Codex', scan.events, generatedAt))
	}

	return {
		snapshot: {
			schemaVersion: SCHEMA_VERSION,
			generatedAt,
			providers,
		},
		scanStats,
	}
}

function optionPaths(paths: string[]): string[] | undefined {
	return paths.length > 0 ? paths : undefined
}

function providerFromDailyReports(
	providerId: 'claude' | 'codex',
	displayName: string,
	events: Parameters<typeof buildDailyReport>[0],
	generatedAt: string,
): ProviderSnapshot {
	const daily = buildDailyReport(events, { order: 'desc' })
	const todayKey = generatedAt.slice(0, 10)
	const yesterdayKey = new Date(new Date(`${todayKey}T00:00:00.000Z`).getTime() - 86_400_000)
		.toISOString()
		.slice(0, 10)
	const today = daily.find((row) => row.date === todayKey)
	const yesterday = daily.find((row) => row.date === yesterdayKey)
	const last30Tokens = daily.slice(0, 30).reduce((sum, row) => sum + row.totalTokens, 0)
	const lines: MetricLine[] = [
		{
			type: 'text',
			label: 'Today',
			value: formatTokens(today?.totalTokens ?? 0),
			provenance: [{ kind: 'local-log', providerId, fetchedAt: generatedAt }],
		},
		{
			type: 'text',
			label: 'Yesterday',
			value: formatTokens(yesterday?.totalTokens ?? 0),
			provenance: [{ kind: 'local-log', providerId, fetchedAt: generatedAt }],
		},
		{
			type: 'text',
			label: 'Last 30 Days',
			value: formatTokens(last30Tokens),
			provenance: [{ kind: 'local-log', providerId, fetchedAt: generatedAt }],
		},
	]

	return {
		providerId,
		displayName,
		lines,
		provenance: [{ kind: 'local-log', providerId, fetchedAt: generatedAt }],
	}
}

function formatTokens(tokens: number): string {
	return Intl.NumberFormat('en-US').format(tokens)
}
