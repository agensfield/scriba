import type { ScribaCache } from '../cache/sqlite.ts'
import type { ScribaConfig } from '../config/schema.ts'
import { iterateCachedClaudeEvents, iterateCachedCodexEvents } from '../local/cached.ts'
import { iterateClaudeEvents } from '../local/claude.ts'
import { iterateCodexEvents } from '../local/codex.ts'
import { emptyScannerStats, type ScannerStats } from '../local/types.ts'
import { PROVIDER_DESCRIPTORS } from '../providers/descriptors.ts'
import { probeClaudeUsage } from '../remote/claude.ts'
import { probeCodexUsage } from '../remote/codex.ts'
import { buildDailyReportFromAsync } from '../reports/stream.ts'
import {
	type MetricLine,
	type ProviderSnapshot,
	SCHEMA_VERSION,
	type StatusSnapshot,
} from '../schema/model.ts'

export type BuildStatusOptions = {
	config: ScribaConfig
	cache?: ScribaCache | undefined
	now?: Date
	includeRemote?: boolean
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
		const stats = emptyScannerStats()
		const paths = optionPaths(options.config.providers.claude.paths)
		const daily = await buildDailyReportFromAsync(
			options.cache == null
				? iterateClaudeEvents({ paths, stats })
				: iterateCachedClaudeEvents({ cache: options.cache, paths, stats }),
			{ order: 'desc' },
		)
		scanStats.claude = stats
		const provider = providerFromDailyReports(
			'claude',
			PROVIDER_DESCRIPTORS.claude.displayName,
			daily,
			stats,
			generatedAt,
		)
		if (options.includeRemote !== false) {
			await appendRemoteLines(provider, () => probeClaudeUsage())
		}
		providers.push(provider)
	}

	if (options.config.providers.codex.enabled) {
		const stats = emptyScannerStats()
		const paths = optionPaths(options.config.providers.codex.paths)
		const daily = await buildDailyReportFromAsync(
			options.cache == null
				? iterateCodexEvents({ paths, stats })
				: iterateCachedCodexEvents({ cache: options.cache, paths, stats }),
			{ order: 'desc' },
		)
		scanStats.codex = stats
		const provider = providerFromDailyReports(
			'codex',
			PROVIDER_DESCRIPTORS.codex.displayName,
			daily,
			stats,
			generatedAt,
		)
		if (options.includeRemote !== false) {
			await appendRemoteLines(provider, () => probeCodexUsage())
		}
		providers.push(provider)
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
	daily: Awaited<ReturnType<typeof buildDailyReportFromAsync>>,
	stats: ScannerStats,
	generatedAt: string,
): ProviderSnapshot {
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
		state: stats.missingDirectories.length > 0 ? 'degraded' : 'ok',
		lines,
		provenance: [{ kind: 'local-log', providerId, fetchedAt: generatedAt }],
	}
}

function formatTokens(tokens: number): string {
	return Intl.NumberFormat('en-US').format(tokens)
}

async function appendRemoteLines(
	provider: ProviderSnapshot,
	probe: () => Promise<{ lines: MetricLine[]; provenance: ProviderSnapshot['provenance'] }>,
): Promise<void> {
	try {
		const remote = await probe()
		provider.lines.unshift(...remote.lines)
		provider.provenance.push(...remote.provenance)
	} catch (error) {
		provider.provenance.push({
			kind: 'provider-api',
			providerId: provider.providerId,
			fetchedAt: new Date().toISOString(),
			error: error instanceof Error ? error.message : String(error),
		})
		provider.state = provider.state === 'broken' ? 'broken' : 'degraded'
	}
}
